package integration_test

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os/exec"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
	"github.com/onsi/gomega/ghttp"
	"github.com/vito/go-sse/sse"

	"github.com/concourse/atc"
	"github.com/concourse/atc/event"
)

var _ = Describe("Fly CLI", func() {
	var buildDir string
	var otherInputDir string

	var streaming chan struct{}
	var events chan atc.Event
	var uploading chan struct{}
	var uploadingTwo chan struct{}

	var expectedPlan atc.Plan

	BeforeEach(func() {
		var err error

		buildDir, err = ioutil.TempDir("", "fly-build-dir")
		Expect(err).NotTo(HaveOccurred())

		otherInputDir, err = ioutil.TempDir("", "fly-s3-asset-dir")
		Expect(err).NotTo(HaveOccurred())

		err = ioutil.WriteFile(
			filepath.Join(buildDir, "task.yml"),
			[]byte(`---
platform: some-platform

image: ubuntu

inputs:
- name: some-input
- name: some-other-input

params:
  FOO: bar
  BAZ: buzz
  X: 1

run:
  path: find
  args: [.]
`),
			0644,
		)
		Expect(err).NotTo(HaveOccurred())

		err = ioutil.WriteFile(
			filepath.Join(otherInputDir, "s3-asset-file"),
			[]byte(`blob`),
			0644,
		)
		Expect(err).NotTo(HaveOccurred())

		streaming = make(chan struct{})
		events = make(chan atc.Event)

		planFactory := atc.NewPlanFactory(0)

		expectedPlan = planFactory.NewPlan(atc.DoPlan{
			planFactory.NewPlan(atc.AggregatePlan{
				planFactory.NewPlan(atc.GetPlan{
					Name: "some-input",
					Type: "archive",
					Source: atc.Source{
						"uri": atcServer.URL() + "/api/v1/pipes/some-pipe-id",
					},
				}),
				planFactory.NewPlan(atc.GetPlan{
					Name: "some-other-input",
					Type: "archive",
					Source: atc.Source{
						"uri": atcServer.URL() + "/api/v1/pipes/some-other-pipe-id",
					},
				}),
			}),
			planFactory.NewPlan(atc.TaskPlan{
				Name: "one-off",
				Config: &atc.TaskConfig{
					Platform: "some-platform",
					Image:    "ubuntu",
					Inputs: []atc.TaskInputConfig{
						{Name: "some-input"},
						{Name: "some-other-input"},
					},
					Params: map[string]string{
						"FOO": "bar",
						"BAZ": "buzz",
						"X":   "1",
					},
					Run: atc.TaskRunConfig{
						Path: "find",
						Args: []string{"."},
					},
				},
			}),
		})
	})

	JustBeforeEach(func() {
		uploading = make(chan struct{})
		uploadingTwo = make(chan struct{})

		atcServer.AppendHandlers(
			ghttp.CombineHandlers(
				ghttp.VerifyRequest("POST", "/api/v1/pipes"),
				ghttp.RespondWithJSONEncoded(http.StatusCreated, atc.Pipe{
					ID:       "some-pipe-id",
					ReadURL:  atcServer.URL() + "/api/v1/pipes/some-pipe-id",
					WriteURL: atcServer.URL() + "/api/v1/pipes/some-pipe-id",
				}),
			),
			ghttp.CombineHandlers(
				ghttp.VerifyRequest("POST", "/api/v1/pipes"),
				ghttp.RespondWithJSONEncoded(http.StatusCreated, atc.Pipe{
					ID:       "some-other-pipe-id",
					ReadURL:  atcServer.URL() + "/api/v1/pipes/some-other-pipe-id",
					WriteURL: atcServer.URL() + "/api/v1/pipes/some-other-pipe-id",
				}),
			),
		)
		atcServer.RouteToHandler("POST", "/api/v1/builds",
			ghttp.CombineHandlers(
				ghttp.VerifyRequest("POST", "/api/v1/builds"),
				VerifyPlan(expectedPlan),
				func(w http.ResponseWriter, r *http.Request) {
					http.SetCookie(w, &http.Cookie{
						Name:    "Some-Cookie",
						Value:   "some-cookie-data",
						Path:    "/",
						Expires: time.Now().Add(1 * time.Minute),
					})
				},
				ghttp.RespondWith(201, `{"id":128}`),
			),
		)
		atcServer.RouteToHandler("GET", "/api/v1/builds/128/events",
			ghttp.CombineHandlers(
				ghttp.VerifyRequest("GET", "/api/v1/builds/128/events"),
				func(w http.ResponseWriter, r *http.Request) {
					flusher := w.(http.Flusher)

					w.Header().Add("Content-Type", "text/event-stream; charset=utf-8")
					w.Header().Add("Cache-Control", "no-cache, no-store, must-revalidate")
					w.Header().Add("Connection", "keep-alive")

					w.WriteHeader(http.StatusOK)

					flusher.Flush()

					close(streaming)

					id := 0

					for e := range events {
						payload, err := json.Marshal(event.Message{Event: e})
						Expect(err).NotTo(HaveOccurred())

						event := sse.Event{
							ID:   fmt.Sprintf("%d", id),
							Name: "event",
							Data: payload,
						}

						err = event.Write(w)
						Expect(err).NotTo(HaveOccurred())

						flusher.Flush()

						id++
					}

					err := sse.Event{
						Name: "end",
					}.Write(w)
					Expect(err).NotTo(HaveOccurred())
				},
			),
		)
		atcServer.RouteToHandler("PUT", "/api/v1/pipes/some-pipe-id",
			ghttp.CombineHandlers(
				ghttp.VerifyRequest("PUT", "/api/v1/pipes/some-pipe-id"),
				func(w http.ResponseWriter, req *http.Request) {
					close(uploading)

					gr, err := gzip.NewReader(req.Body)
					Expect(err).NotTo(HaveOccurred())

					tr := tar.NewReader(gr)

					hdr, err := tr.Next()
					Expect(err).NotTo(HaveOccurred())

					Expect(hdr.Name).To(Equal("./"))

					hdr, err = tr.Next()
					Expect(err).NotTo(HaveOccurred())

					Expect(hdr.Name).To(MatchRegexp("(./)?task.yml$"))
				},
				ghttp.RespondWith(200, ""),
			),
		)
		atcServer.RouteToHandler("PUT", "/api/v1/pipes/some-other-pipe-id",
			ghttp.CombineHandlers(
				ghttp.VerifyRequest("PUT", "/api/v1/pipes/some-other-pipe-id"),
				func(w http.ResponseWriter, req *http.Request) {
					close(uploadingTwo)

					gr, err := gzip.NewReader(req.Body)
					Expect(err).NotTo(HaveOccurred())

					tr := tar.NewReader(gr)

					hdr, err := tr.Next()
					Expect(err).NotTo(HaveOccurred())

					Expect(hdr.Name).To(Equal("./"))

					hdr, err = tr.Next()
					Expect(err).NotTo(HaveOccurred())

					Expect(hdr.Name).To(MatchRegexp("(./)?s3-asset-file$"))
				},
				ghttp.RespondWith(200, ""),
			),
		)
	})

	It("flies with multiple passengers", func() {
		flyCmd := exec.Command(
			flyPath, "-t", targetName, "e",
			"--input", fmt.Sprintf("some-input=%s", buildDir),
			"--input", fmt.Sprintf("some-other-input=%s", otherInputDir),
			"--config", filepath.Join(buildDir, "task.yml"),
		)

		sess, err := gexec.Start(flyCmd, GinkgoWriter, GinkgoWriter)
		Expect(err).NotTo(HaveOccurred())

		Eventually(streaming).Should(BeClosed())
		Eventually(uploading).Should(BeClosed())
		Eventually(uploadingTwo).Should(BeClosed())

		events <- event.Log{Payload: "sup"}
		close(events)

		Eventually(sess.Out).Should(gbytes.Say("sup"))

		<-sess.Exited
		Expect(sess).To(gexec.Exit(0))
	})
})
