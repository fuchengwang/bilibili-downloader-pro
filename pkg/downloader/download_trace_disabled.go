//go:build !dev || production || bindings

package downloader

import "net/http"

const downloadTraceEnabled = false

type downloadTrace struct{}
type downloadTraceRequest struct{}
type downloadTraceSource struct{}

func startDownloadTrace(string) downloadTrace { return downloadTrace{} }
func (downloadTrace) active() bool            { return false }
func (downloadTrace) forTask(*DownloadTask, string) downloadTrace {
	return downloadTrace{}
}
func (downloadTrace) forTrack(string) downloadTrace { return downloadTrace{} }
func (downloadTrace) event(string, map[string]any)  {}
func (downloadTrace) request(*http.Request, string, map[string]any) downloadTraceRequest {
	return downloadTraceRequest{}
}
func (downloadTraceRequest) event(string, map[string]any)                {}
func (downloadTraceRequest) response(*http.Response, error)              {}
func (downloadTraceRequest) withContext(req *http.Request) *http.Request { return req }
func traceMediaRoute(*http.Request, string, string, error)               {}
func traceSource(string) downloadTraceSource                             { return downloadTraceSource{} }
func traceSources([]string) []downloadTraceSource                        { return nil }
func traceFingerprint(string) string                                     { return "" }
func traceError(error) string                                            { return "" }
func (*rangeTransfer) traceCheckpoint(string, error)                     {}
