package resource_test

import (
	"errors"
	"io/ioutil"

	"github.com/cloudfoundry-incubator/garden/warden"
	wfakes "github.com/cloudfoundry-incubator/garden/warden/fakes"
	"github.com/concourse/turbine/api/builds"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("Resource Check", func() {
	var (
		input builds.Input

		checkScriptStdout     string
		checkScriptStderr     string
		checkScriptExitStatus int
		runCheckError         error

		checkScriptProcess *wfakes.FakeProcess

		checkResult []builds.Version
		checkErr    error
	)

	BeforeEach(func() {
		input = builds.Input{
			Type:    "some-resource",
			Source:  builds.Source{"some": "source"},
			Version: builds.Version{"some": "version"},
		}

		checkScriptStdout = "[]"
		checkScriptStderr = ""
		checkScriptExitStatus = 0
		runCheckError = nil

		checkScriptProcess = new(wfakes.FakeProcess)
		checkScriptProcess.WaitStub = func() (int, error) {
			return checkScriptExitStatus, nil
		}

		checkResult = nil
		checkErr = nil
	})

	JustBeforeEach(func() {
		wardenClient.Connection.RunStub = func(handle string, spec warden.ProcessSpec, io warden.ProcessIO) (warden.Process, error) {
			if runCheckError != nil {
				return nil, runCheckError
			}

			_, err := io.Stdout.Write([]byte(checkScriptStdout))
			Ω(err).ShouldNot(HaveOccurred())

			_, err = io.Stderr.Write([]byte(checkScriptStderr))
			Ω(err).ShouldNot(HaveOccurred())

			return checkScriptProcess, nil
		}

		checkResult, checkErr = resource.Check(input)
	})

	It("runs /opt/resource/check the request on stdin", func() {
		Ω(checkErr).ShouldNot(HaveOccurred())

		handle, spec, io := wardenClient.Connection.RunArgsForCall(0)
		Ω(handle).Should(Equal("some-handle"))
		Ω(spec.Path).Should(Equal("/opt/resource/check"))
		Ω(spec.Args).Should(BeEmpty())
		Ω(spec.Privileged).Should(BeTrue())

		request, err := ioutil.ReadAll(io.Stdin)
		Ω(err).ShouldNot(HaveOccurred())

		Ω(string(request)).Should(Equal(`{"version":{"some":"version"},"source":{"some":"source"}}`))
	})

	Context("when /check outputs versions", func() {
		BeforeEach(func() {
			checkScriptStdout = `[{"ver":"abc"}, {"ver":"def"}, {"ver":"ghi"}]`
		})

		It("returns the raw parsed contents", func() {
			Ω(checkErr).ShouldNot(HaveOccurred())

			Ω(checkResult).Should(Equal([]builds.Version{
				builds.Version{"ver": "abc"},
				builds.Version{"ver": "def"},
				builds.Version{"ver": "ghi"},
			}))
		})
	})

	Context("when running /opt/resource/check fails", func() {
		disaster := errors.New("oh no!")

		BeforeEach(func() {
			runCheckError = disaster
		})

		It("returns an err containing stdout/stderr of the process", func() {
			Ω(checkErr).Should(Equal(disaster))
		})
	})

	Context("when /opt/resource/check exits nonzero", func() {
		BeforeEach(func() {
			checkScriptStdout = "some-stdout-data"
			checkScriptStderr = "some-stderr-data"
			checkScriptExitStatus = 9
		})

		It("returns an err containing stdout/stderr of the process", func() {
			Ω(checkErr).Should(HaveOccurred())

			Ω(checkErr.Error()).Should(ContainSubstring("some-stdout-data"))
			Ω(checkErr.Error()).Should(ContainSubstring("some-stderr-data"))
			Ω(checkErr.Error()).Should(ContainSubstring("exit status 9"))
		})
	})

	Context("when the output of /opt/resource/check is malformed", func() {
		BeforeEach(func() {
			checkScriptStdout = "ß"
		})

		It("returns an error", func() {
			Ω(checkErr).Should(HaveOccurred())
		})
	})
})