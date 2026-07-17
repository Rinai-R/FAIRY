package companion

import (
	"encoding/json"
	"fmt"
	"io"
)

func writeChatTextDelta(w io.Writer, text string) {
	encoded, err := json.Marshal(text)
	if err != nil {
		panic(err)
	}
	fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%s}}]}\n\n", encoded)
}

func writeChatStop(w io.Writer) {
	fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
}
