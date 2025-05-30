package ocmlogger

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("logger.Extra", Label("logger"), func() {
	var (
		ulog   OCMLogger
		output ThreadSafeBytesBuffer
	)

	BeforeEach(func() {
		ulog = NewOCMLogger(context.Background())
		output = WrapUnsafeWriterWithLocks(&bytes.Buffer{})
		SetOutput(output)
		DeferCleanup(func() {
			SetOutput(os.Stderr)
		})
	})

	Context("basic; no Extra", func() {
		It("ignores Info level (default min level is Warning)", func() {
			ulog.Info("message")

			result := output.String()
			Expect(result).To(Equal(""))
		})

		It("includes just message", func() {
			ulog.Warning("message")

			result := output.String()
			Expect(result).NotTo(ContainSubstring("\"Extra\":{"))
			Expect(result).To(ContainSubstring("\"message\":\"message\""))
		})
	})

	Context("awareness of simple types", func() {
		It("all simple types are in keysAndValues", func() {
			ulog.Contextual().Error(
				nil,
				"message",
				"true", true,
				"false", false,
				"int", 1,
				"int8", int8(8),
				"int16", int16(16),
				"int32", int32(32),
				"float32", float32(32.01),
				"float64", 64.01,
			)

			resultBytes, err := io.ReadAll(output)
			Expect(err).NotTo(HaveOccurred())
			result := string(resultBytes)

			Expect(result).To(ContainSubstring("\"Extra\":{"))
			Expect(result).To(ContainSubstring("\"true\":true"))
			Expect(result).To(ContainSubstring("\"false\":false"))
			Expect(result).To(ContainSubstring("\"int\":1"))
			Expect(result).To(ContainSubstring("\"int8\":8"))
			Expect(result).To(ContainSubstring("\"int16\":16"))
			Expect(result).To(ContainSubstring("\"int32\":32"))
			Expect(result).To(ContainSubstring("\"float32\":32.01"))
			Expect(result).To(ContainSubstring("\"float64\":64.01"))
		})
	})

	Context("setting same key", func() {
		It("overrides value in keysAndValues", func() {
			ulog.Contextual().Error(
				nil,
				"warning",
				"key1", 1,
				"key1", 2,
			)
			result := output.String()
			Expect(result).To(ContainSubstring("\"key1\":2"))
		})
	})

	Context("complex/nested types", func() {
		It("each will present in output from keysAndValues", func() {
			headers1 := http.Header{}
			headers1["Content-Type"] = []string{"application/json"}
			headers1["Content-Length"] = []string{"0"}

			resp1 := http.Response{
				StatusCode: 200,
				Header:     headers1,
			}

			headers2 := http.Header{}
			headers2["Content-Type"] = []string{"application/xml"}
			headers2["Content-Length"] = []string{"100"}
			resp2 := http.Response{
				StatusCode: 404,
				Header:     headers2,
			}

			ulog.Contextual().Error(
				nil,
				"warning",
				"resp1", resp1,
				"resp2", resp2,
			)

			result := output.String()
			Expect(result).To(ContainSubstring("\"resp1\":{"))
			Expect(result).To(ContainSubstring("\"resp2\":{"))
			Expect(result).To(ContainSubstring("\"StatusCode\":200"))
			Expect(result).To(ContainSubstring("\"StatusCode\":404"))
			Expect(result).To(ContainSubstring("\"Header\":{\"Content-Length\":[\"0\"],\"Content-Type\":[\"application/json\"]}"))
			Expect(result).To(ContainSubstring("\"Header\":{\"Content-Length\":[\"100\"],\"Content-Type\":[\"application/xml\"]}"))
			Expect(result).To(ContainSubstring("\"StatusCode\":404"))
		})
	})

	Context("Error", func() {
		It("adds error message, sets level to error, non-racy", func() {
			ulog.CaptureSentryEvent(false).Contextual().Error(fmt.Errorf("error-message"), "ERROR")

			result := output.String()
			Expect(result).To(ContainSubstring("\"level\":\"error\","))
			Expect(result).To(ContainSubstring("\"error\":\"error-message\","))
			Expect(result).To(ContainSubstring("\"message\":\"ERROR\""))
		})
	})

	Context("registered context keys are added to output", func() {
		BeforeEach(func() {
			getOpIdFromContext := func(ctx context.Context) any {
				return ctx.Value("opID")
			}
			getTxIdFromContext := func(ctx context.Context) any {
				return ctx.Value("tx_id")
			}

			RegisterExtraDataCallback("opID", getOpIdFromContext)
			RegisterExtraDataCallback("tx_id", getTxIdFromContext)

			ctx := context.Background()

			//lint:ignore SA1029 doesnt matter for a test
			ctx = context.WithValue(ctx, "opID", "OpId1")

			//lint:ignore SA1029 doesnt matter for a test
			ctx = context.WithValue(ctx, "tx_id", int64(123))
			ulog = NewOCMLogger(ctx)

			DeferCleanup(ClearExtraDataCallbacks)
		})

		It("each one is added to output", func() {
			ulog.Warning("warning")

			result := output.String()
			Expect(result).To(ContainSubstring("\"opID\":\"OpId1\""))
			Expect(result).To(ContainSubstring("\"tx_id\":123"))
		})

		It("nil function safe", func() {
			ulog.Warning("warning")
			RegisterExtraDataCallback("nilCallbackFunction", nil)

			result := output.String()
			Expect(result).To(ContainSubstring("\"opID\":\"OpId1\""))
			Expect(result).To(ContainSubstring("\"tx_id\":123"))
			Expect(result).NotTo(ContainSubstring("nilCallbackFunction"))
		})

		It("empty callback map safe", func() {
			ClearExtraDataCallbacks()
			ulog.Warning("warning")

			result := output.String()
			Expect(result).NotTo(ContainSubstring("\"Extra\""))
		})
	})
})

var _ = Describe("logger chaos", Label("logger"), func() {

	BeforeEach(func() {
		SetOutput(io.Discard)
		DeferCleanup(func() {
			SetOutput(os.Stderr)
		})
	})

	Context("Chaos", func() {
		// Notes:
		//	* without locks in place I can reliably produce concurrency issues with as few as 50 iterations
		// 	* 10000 iterations takes about 0.1 seconds on my laptop
		// 	* not advised to crank this too high, above 1000000 on my laptop used >100% cpu and 40 gigs of ram
		//    and weird stuff started to happen before go shot itself to save the system
		maxChaos := 10000
		It("AdditionalCallLevelSkips() is thread safe", func() {
			parallelLog := NewOCMLogger(context.Background())

			waitForTestEnd := sync.WaitGroup{}
			for i := 0; i < maxChaos; i++ {
				waitForTestEnd.Add(1)
				go func(i int) {
					defer waitForTestEnd.Done()
					parallelLog.AdditionalCallLevelSkips(0).Info("AdditionalCallLevelSkips() %d", i)
				}(i)
			}
			waitForTestEnd.Wait()
		})
		It("CaptureSentryEvent() is thread safe", func() {
			parallelLog := NewOCMLogger(context.Background())

			waitForTestEnd := sync.WaitGroup{}
			for i := 0; i < maxChaos; i++ {
				waitForTestEnd.Add(1)
				go func(i int) {
					defer waitForTestEnd.Done()
					parallelLog.CaptureSentryEvent(false).Info("CaptureSentryEvent() %d", i)
				}(i)
			}
			waitForTestEnd.Wait()
		})
		It("Contextual().Error() is thread safe", func() {
			parallelLog := NewOCMLogger(context.Background())

			waitForTestEnd := sync.WaitGroup{}
			for i := 0; i < maxChaos; i++ {
				waitForTestEnd.Add(1)
				go func(i int) {
					defer waitForTestEnd.Done()

					parallelLog.Contextual().Error(fmt.Errorf("err %d", i), fmt.Sprintf("Err() %d", i))
				}(i)
			}
			waitForTestEnd.Wait()
		})
		It("Contextual() Lots of extras and an error for fun", func() {
			parallelLog := NewOCMLogger(context.Background())
			maxExtras := 100

			waitForTestEnd := sync.WaitGroup{}
			for i := 0; i < maxChaos; i++ {
				waitForTestEnd.Add(1)
				go func(i int) {
					defer waitForTestEnd.Done()

					kv := []interface{}{}
					for j := 0; j < maxExtras; j++ {
						kv = append(kv, fmt.Sprintf("%d-%d", i, j), i+j)
					}
					parallelLog.Contextual().Error(fmt.Errorf("err %d", i), fmt.Sprintf("Lots of extras %d", i), kv)
				}(i)
			}
			waitForTestEnd.Wait()
		})
	})
})

func TestLoggerNotString(t *testing.T) {
	ulog := NewOCMLogger(context.Background())
	output := bytes.Buffer{}
	SetOutput(&output)
	defer func() {
		SetOutput(os.Stderr)
	}()

	// the cast for this call failed and panicked at one time
	ulog.Error(fmt.Errorf("not a string"))
	ulog.Contextual().Error(nil, "")
}

func TestLoggerMultipleArgs(t *testing.T) {
	ulog := NewOCMLogger(context.Background())
	output := bytes.Buffer{}
	SetOutput(&output)
	defer func() {
		SetOutput(os.Stderr)
	}()

	// the cast for this call failed and panicked at one time
	ulog.Warning("format with %s %s value", "more than one", "argument")
	content, err := io.ReadAll(&output)
	if err != nil {
		t.Fatal(err)
	}
	contentStr := string(content)
	if !strings.Contains(contentStr, "format with more than one argument value") {
		t.Error(contentStr)
	}
}
