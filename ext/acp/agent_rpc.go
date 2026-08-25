package acp

import (
	"context"
	"encoding/json"
	"fmt"
)

// incomingResponse is a client reply to an agent→client request
// (session/request_permission). Distinct from the outgoing response type,
// which encodes Result as a Go value.
type incomingResponse struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

var errClientMethod = fmt.Errorf("acp: client method not supported")

// callClient sends a JSON-RPC request to the editor and waits for the matching
// response. The serve loop must keep reading stdin or this blocks forever.
func (a *agentServer) callClient(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if a == nil {
		return nil, fmt.Errorf("acp: nil agent")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	id := fmt.Sprintf("acp-%d", a.nextID.Add(1))
	rawID, err := json.Marshal(id)
	if err != nil {
		return nil, err
	}
	key := string(rawID)
	ch := make(chan incomingResponse, 1)
	a.mu.Lock()
	if a.pending == nil {
		a.pending = map[string]chan incomingResponse{}
	}
	a.pending[key] = ch
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.pending, key)
		a.mu.Unlock()
	}()

	a.write(request{
		JSONRPC: "2.0",
		ID:      rawID,
		Method:  method,
		Params:  mustJSON(params),
	})

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case resp := <-ch:
		if resp.Error != nil {
			if resp.Error.Code == errMethod {
				return nil, errClientMethod
			}
			return nil, fmt.Errorf("%s", resp.Error.Message)
		}
		return resp.Result, nil
	}
}

func (a *agentServer) deliverClientResponse(line []byte) {
	var resp incomingResponse
	if err := json.Unmarshal(line, &resp); err != nil || len(resp.ID) == 0 {
		return
	}
	a.mu.Lock()
	ch := a.pending[string(resp.ID)]
	a.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- resp:
	default:
	}
}
