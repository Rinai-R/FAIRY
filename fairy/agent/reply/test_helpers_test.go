package reply

import "encoding/json"

type testReplyChain struct {
	VisualState string `json:"visualState"`
	Text        string `json:"text"`
}

func testRespondEnvelope(chains ...testReplyChain) string {
	payload := struct {
		Chains []testReplyChain `json:"chains"`
	}{
		Chains: chains,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func visualStates(ids ...string) []VisualState {
	states := make([]VisualState, 0, len(ids))
	for _, id := range ids {
		states = append(states, VisualState{ID: id, Description: id + " 状态说明"})
	}
	return states
}
