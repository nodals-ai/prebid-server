// Package nodals implements a Prebid Server module for Nodals RTD
package nodals

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/prebid/openrtb/v20/openrtb2"
	"github.com/prebid/prebid-server/v4/hooks/hookanalytics"
	"github.com/prebid/prebid-server/v4/hooks/hookstage"
	"github.com/prebid/prebid-server/v4/logger"
	"github.com/prebid/prebid-server/v4/modules/moduledeps"
	"github.com/prebid/prebid-server/v4/util/iterutil"
	"github.com/prebid/prebid-server/v4/util/jsonutil"
	"github.com/tidwall/sjson"
)

// Builder is the entry point for the module
// This is called by Prebid Server to initialize the module
func Builder(config json.RawMessage, deps moduledeps.ModuleDeps) (interface{}, error) {
	var cfg Config
	if err := jsonutil.Unmarshal(config, &cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	if cfg.Endpoint == "" {
		return nil, fmt.Errorf("endpoint is required in nodals rtd module config")
	}
	if cfg.PropertyID == "" {
		return nil, fmt.Errorf("property_id is required in nodals rtd module config")
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 500 // 500ms default
	}

	return &Module{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout:   time.Duration(cfg.Timeout) * time.Millisecond,
			Transport: deps.HTTPClient.Transport,
		},
	}, nil
}

const (
	// keys for module context
	asyncRequestKey = "nodals.AsyncRequest"
)

var (
	// Declare hooks
	_ hookstage.Entrypoint              = (*Module)(nil)
	_ hookstage.ProcessedAuctionRequest = (*Module)(nil)
	_ hookstage.AuctionResponse         = (*Module)(nil)
)

// Config holds module configuration
type Config struct {
	Endpoint   string `json:"endpoint"`    // Required: URL of the Nodals targeting server
	PropertyID string `json:"property_id"` // Required: Publisher's property identifier
	Timeout    int    `json:"timeout_ms"`  // Optional: HTTP timeout in milliseconds (default: 1000)
}

// TargetingKeyValue represents a single key-value pair for targeting
type TargetingKeyValue struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// ServerResponse represents the response from the Nodals server
type ServerResponse struct {
	RequestID string                         `json:"request_id"`
	Targeting map[string][]TargetingKeyValue `json:"targeting"` // impression_id -> key-values
}

// Module implements the Nodals RTD module
type Module struct {
	cfg        Config
	httpClient *http.Client
}

// HandleEntrypointHook initializes the module context with an async request
func (m *Module) HandleEntrypointHook(
	ctx context.Context,
	miCtx hookstage.ModuleInvocationContext,
	payload hookstage.EntrypointPayload,
) (hookstage.HookResult[hookstage.EntrypointPayload], error) {
	// Initialize module context with async request
	return hookstage.HookResult[hookstage.EntrypointPayload]{
		ModuleContext: hookstage.ModuleContext{
			asyncRequestKey: m.NewAsyncRequest(payload.Request),
		},
	}, nil
}

// HandleProcessedAuctionHook is called early in the auction to fetch targeting data
func (m *Module) HandleProcessedAuctionHook(
	ctx context.Context,
	miCtx hookstage.ModuleInvocationContext,
	payload hookstage.ProcessedAuctionRequestPayload,
) (hookstage.HookResult[hookstage.ProcessedAuctionRequestPayload], error) {
	var ret hookstage.HookResult[hookstage.ProcessedAuctionRequestPayload]
	analyticsNamePrefix := "HandleProcessedAuctionHook."

	asyncRequest, ok := miCtx.ModuleContext[asyncRequestKey].(*AsyncRequest)
	if !ok {
		ret.AnalyticsTags = hookanalytics.Analytics{
			Activities: []hookanalytics.Activity{{
				Name:   analyticsNamePrefix + asyncRequestKey,
				Status: hookanalytics.ActivityStatusError,
				Results: []hookanalytics.Result{{
					Status: hookanalytics.ResultStatusError,
					Values: map[string]interface{}{"error": "failed to get async request from module context"},
				}},
			}},
		}
		return ret, nil
	}

	// Start async request to Nodals server
	asyncRequest.fetchTargetingAsync(payload.Request.BidRequest)

	return ret, nil
}

// HandleAuctionResponseHook adds targeting data to the auction response
func (m *Module) HandleAuctionResponseHook(
	ctx context.Context,
	miCtx hookstage.ModuleInvocationContext,
	payload hookstage.AuctionResponsePayload,
) (hookstage.HookResult[hookstage.AuctionResponsePayload], error) {
	analyticsNamePrefix := "HandleAuctionResponseHook."
	var ret hookstage.HookResult[hookstage.AuctionResponsePayload]

	asyncRequest, ok := miCtx.ModuleContext[asyncRequestKey].(*AsyncRequest)
	if !ok {
		ret.AnalyticsTags = hookanalytics.Analytics{
			Activities: []hookanalytics.Activity{{
				Name:   analyticsNamePrefix + asyncRequestKey,
				Status: hookanalytics.ActivityStatusError,
				Results: []hookanalytics.Result{{
					Status: hookanalytics.ResultStatusError,
					Values: map[string]interface{}{"error": "failed to get async request from module context"},
				}},
			}},
		}
		return ret, nil
	}

	// Ensure we cancel the request context always to free resources
	defer asyncRequest.Cancel()

	// Check if a request was made
	if asyncRequest.Done == nil {
		return ret, nil
	}

	// Wait for the async request to complete
	select {
	case <-asyncRequest.Done:
		// Request completed
	case <-ctx.Done():
		// Context cancelled, exit gracefully
		return ret, nil
	}

	// Get results
	targeting, err := asyncRequest.Targeting, asyncRequest.Err
	if err != nil {
		ret.AnalyticsTags = hookanalytics.Analytics{
			Activities: []hookanalytics.Activity{{
				Name:   analyticsNamePrefix + "nodals_fetch",
				Status: hookanalytics.ActivityStatusError,
				Results: []hookanalytics.Result{{
					Status: hookanalytics.ResultStatusError,
					Values: map[string]interface{}{"error": err.Error()},
				}},
			}},
		}
		return ret, nil
	}

	if len(targeting) == 0 {
		return ret, nil
	}

	// Add targeting to the auction response
	ret.ChangeSet.AddMutation(
		func(payload hookstage.AuctionResponsePayload) (hookstage.AuctionResponsePayload, error) {
			// Apply targeting to each bid based on its impression ID
			for seatBid := range iterutil.SlicePointerValues(payload.BidResponse.SeatBid) {
				for bid := range iterutil.SlicePointerValues(seatBid.Bid) {
					// Get targeting for this impression
					kvPairs, exists := targeting[bid.ImpID]
					if !exists || len(kvPairs) == 0 {
						continue
					}

					// Merge duplicate keys
					merged := mergeDuplicateKeys(kvPairs)

					// Apply each key with comma-separated values
					for key, value := range merged {
						newPayload, err := sjson.SetBytes(bid.Ext, "prebid.targeting."+key, value)
						if err != nil {
							logger.Errorf("Failed to add targeting to bid: %v", err)
							continue
						}
						bid.Ext = newPayload
					}
				}
			}

			return payload, nil
		},
		hookstage.MutationUpdate,
		"ext",
	)

	return ret, nil
}

// fetchTargeting calls the Nodals server API and returns targeting data per impression
func (m *Module) fetchTargeting(ctx context.Context, bidRequest *openrtb2.BidRequest) (map[string][]TargetingKeyValue, error) {
	// Marshal the bid request
	requestBody, err := jsonutil.Marshal(bidRequest)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal bid request: %w", err)
	}

	// Inject property_id into ext.nodals.property_id
	requestBody, err = sjson.SetBytes(requestBody, "ext.nodals.property_id", m.cfg.PropertyID)
	if err != nil {
		return nil, fmt.Errorf("failed to add property_id to request: %w", err)
	}

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, "POST", m.cfg.Endpoint, bytes.NewReader(requestBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create http request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	// Make the request
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server returned status %d", resp.StatusCode)
	}

	// Parse response
	var serverResp ServerResponse
	if err = json.NewDecoder(resp.Body).Decode(&serverResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return serverResp.Targeting, nil
}

// mergeDuplicateKeys consolidates duplicate keys with comma-separated values
// Example: [{"key":"cat","value":"tech"}, {"key":"cat","value":"news"}] -> {"cat": "tech,news"}
func mergeDuplicateKeys(kvPairs []TargetingKeyValue) map[string]string {
	merged := make(map[string][]string)

	// Group values by key
	for _, kv := range kvPairs {
		merged[kv.Key] = append(merged[kv.Key], kv.Value)
	}

	// Join values with commas
	result := make(map[string]string)
	for key, values := range merged {
		result[key] = strings.Join(values, ",")
	}

	return result
}
