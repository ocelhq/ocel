package main

import (
	"time"

	"github.com/proxy-wasm/proxy-wasm-go-sdk/proxywasm"
	"github.com/proxy-wasm/proxy-wasm-go-sdk/proxywasm/types"

	"github.com/ocelhq/ocel/platform/gcp/gateplugin"
)

func main() {}

func init() { proxywasm.SetVMContext(&vmContext{}) }

type vmContext struct{ types.DefaultVMContext }

func (*vmContext) NewPluginContext(uint32) types.PluginContext { return &pluginContext{} }

type pluginContext struct {
	types.DefaultPluginContext
	config gateplugin.Config
}

func (p *pluginContext) OnPluginStart(int) types.OnPluginStartStatus {
	data, err := proxywasm.GetPluginConfiguration()
	if err != nil {
		proxywasm.LogCritical("the plugin holds no config")
		return types.OnPluginStartStatusFailed
	}
	config, err := gateplugin.ParseConfig(string(data))
	if err != nil {
		proxywasm.LogCritical(err.Error())
		return types.OnPluginStartStatusFailed
	}
	p.config = config
	return types.OnPluginStartStatusOK
}

func (p *pluginContext) NewHttpContext(uint32) types.HttpContext {
	return &httpContext{config: &p.config}
}

type httpContext struct {
	types.DefaultHttpContext
	config *gateplugin.Config
}

func (h *httpContext) OnHttpRequestHeaders(int, bool) types.Action {
	pairs, err := proxywasm.GetHttpRequestHeaders()
	if err != nil {
		answer(500, map[string][]string{gateplugin.RouterHeader: {gateplugin.RouterKind}})
		return types.ActionPause
	}
	req := gateplugin.ReadRequest(pairs)
	verdict := h.config.Check(req, time.Now())
	if response := verdict.Response; response != nil {
		answer(response.Status, response.Header, []byte(response.Body)...)
		return types.ActionPause
	}
	remove, replace := gateplugin.ListHeaderEdits(req.Header, verdict.Forward)
	for _, name := range remove {
		_ = proxywasm.RemoveHttpRequestHeader(name)
	}
	for name, value := range replace {
		_ = proxywasm.ReplaceHttpRequestHeader(name, value)
	}
	return types.ActionContinue
}

func answer(status int, header map[string][]string, body ...byte) {
	var pairs [][2]string
	for name, values := range header {
		for _, value := range values {
			pairs = append(pairs, [2]string{name, value})
		}
	}
	_ = proxywasm.SendHttpResponse(uint32(status), pairs, body, -1)
}
