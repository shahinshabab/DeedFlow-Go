package pipeline

import (
	"encoding/json"

	"github.com/shahinshabab/deedflow-go/internal/ai"
)

// marshalExtracted is ExtractedDeedData.model_dump(mode="json").
func marshalExtracted(e *ai.ExtractedDeedData) (json.RawMessage, error) {
	return json.Marshal(e)
}
