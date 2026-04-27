package nodals

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prebid/openrtb/v20/openrtb2"
	"github.com/prebid/prebid-server/v4/hooks/hookstage"
	"github.com/prebid/prebid-server/v4/modules/moduledeps"
	"github.com/prebid/prebid-server/v4/openrtb_ext"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func getTestModuleDeps(t *testing.T) moduledeps.ModuleDeps {
	t.Helper()
	return moduledeps.ModuleDeps{
		HTTPClient: http.DefaultClient,
	}
}

func getTestEntrypointPayload(t *testing.T) hookstage.EntrypointPayload {
	body := []byte(`{}`)
	return hookstage.EntrypointPayload{
		Request: httptest.NewRequest(http.MethodPost, "/openrtb2/auction", bytes.NewBuffer(body)),
		Body:    body,
	}
}

func TestBuilder(t *testing.T) {
	config := json.RawMessage(`{
		"endpoint": "https://nodals-server.com/api/targeting",
		"property_id": "test-property-123",
		"timeout_ms": 1500
	}`)

	deps := moduledeps.ModuleDeps{HTTPClient: http.DefaultClient}
	module, err := Builder(config, deps)

	assert.NoError(t, err)
	assert.NotNil(t, module)
	assert.IsType(t, &Module{}, module)

	m := module.(*Module)
	assert.Equal(t, "https://nodals-server.com/api/targeting", m.cfg.Endpoint)
	assert.Equal(t, "test-property-123", m.cfg.PropertyID)
	assert.Equal(t, 1500, m.cfg.Timeout)
}

func TestBuilderDefaults(t *testing.T) {
	config := json.RawMessage(`{
		"endpoint": "https://nodals-server.com/api/targeting",
		"property_id": "test-property-123"
	}`)

	deps := moduledeps.ModuleDeps{HTTPClient: http.DefaultClient}
	module, err := Builder(config, deps)

	assert.NoError(t, err)
	m := module.(*Module)
	assert.Equal(t, "https://nodals-server.com/api/targeting", m.cfg.Endpoint)
	assert.Equal(t, "test-property-123", m.cfg.PropertyID)
	assert.Equal(t, 500, m.cfg.Timeout) // Default timeout
}

func TestBuilderMissingEndpoint(t *testing.T) {
	config := json.RawMessage(`{
		"property_id": "test-property-123",
		"timeout_ms": 1000
	}`)

	deps := moduledeps.ModuleDeps{HTTPClient: http.DefaultClient}
	module, err := Builder(config, deps)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "endpoint is required")
	assert.Nil(t, module)
}

func TestBuilderMissingPropertyID(t *testing.T) {
	config := json.RawMessage(`{
		"endpoint": "https://nodals-server.com/api/targeting",
		"timeout_ms": 1000
	}`)

	deps := moduledeps.ModuleDeps{HTTPClient: http.DefaultClient}
	module, err := Builder(config, deps)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "property_id is required")
	assert.Nil(t, module)
}

func TestBuilderInvalidConfig(t *testing.T) {
	config := json.RawMessage(`invalid json`)
	deps := moduledeps.ModuleDeps{HTTPClient: http.DefaultClient}

	module, err := Builder(config, deps)

	assert.Error(t, err)
	assert.Nil(t, module)
}

func TestHandleEntrypointHook(t *testing.T) {
	module := &Module{}
	ctx := context.Background()
	miCtx := hookstage.ModuleInvocationContext{}
	payload := getTestEntrypointPayload(t)

	result, err := module.HandleEntrypointHook(ctx, miCtx, payload)

	assert.NoError(t, err)
	assert.NotNil(t, result.ModuleContext[asyncRequestKey])
}

func TestAPIIntegration(t *testing.T) {
	// Create mock Nodals API server
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request method and headers
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		// Verify property_id is in the request
		var requestBody map[string]interface{}
		err := json.NewDecoder(r.Body).Decode(&requestBody)
		require.NoError(t, err)

		ext, ok := requestBody["ext"].(map[string]interface{})
		require.True(t, ok, "ext should exist in request")
		nodals, ok := ext["nodals"].(map[string]interface{})
		require.True(t, ok, "ext.nodals should exist in request")
		assert.Equal(t, "e6b50d21", nodals["property_id"], "property_id should be in ext.nodals")

		// Return mock Nodals response with targeting per impression
		response := `{
			"request_id": "test-auction",
			"targeting": {
				"test-imp-1": [
					{"key": "category", "value": "technology"},
					{"key": "category", "value": "news"},
					{"key": "sentiment", "value": "positive"}
				],
				"test-imp-2": [
					{"key": "category", "value": "sports"},
					{"key": "position", "value": "above_fold"}
				]
			}
		}`
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(response))
	}))
	defer mockServer.Close()

	// Create module with mock server endpoint
	config := json.RawMessage(`{
		"endpoint": "` + mockServer.URL + `",
		"property_id": "e6b50d21",
		"timeout_ms": 1000
	}`)

	moduleInterface, err := Builder(config, getTestModuleDeps(t))
	require.NoError(t, err)
	module := moduleInterface.(*Module)

	// Create test bid request with 2 impressions
	width := int64(300)
	height := int64(250)
	bidRequest := &openrtb2.BidRequest{
		ID: "test-auction",
		Imp: []openrtb2.Imp{
			{
				ID:     "test-imp-1",
				Banner: &openrtb2.Banner{W: &width, H: &height},
			},
			{
				ID:     "test-imp-2",
				Banner: &openrtb2.Banner{W: &width, H: &height},
			},
		},
		Site: &openrtb2.Site{
			Domain: "example.com",
			Page:   "https://example.com/test-page",
		},
	}

	// Test fetchTargeting
	ctx := context.Background()
	targeting, err := module.fetchTargeting(ctx, bidRequest)
	require.NoError(t, err)
	assert.Len(t, targeting, 2)

	// Verify imp-1 targeting
	assert.Len(t, targeting["test-imp-1"], 3)
	assert.Equal(t, "category", targeting["test-imp-1"][0].Key)
	assert.Equal(t, "technology", targeting["test-imp-1"][0].Value)

	// Verify imp-2 targeting
	assert.Len(t, targeting["test-imp-2"], 2)
	assert.Equal(t, "category", targeting["test-imp-2"][0].Key)
	assert.Equal(t, "sports", targeting["test-imp-2"][0].Value)
}

func TestFullHookWorkflow(t *testing.T) {
	// Create mock server that returns different targeting per impression
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := `{
			"request_id": "test-auction",
			"targeting": {
				"imp-1": [
					{"key": "cat", "value": "tech"},
					{"key": "cat", "value": "news"},
					{"key": "sentiment", "value": "positive"}
				],
				"imp-2": [
					{"key": "cat", "value": "sports"}
				]
			}
		}`
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(response))
	}))
	defer mockServer.Close()

	// Create module
	config := json.RawMessage(`{
		"endpoint": "` + mockServer.URL + `",
		"property_id": "test-property-456",
		"timeout_ms": 1000
	}`)

	moduleInterface, err := Builder(config, getTestModuleDeps(t))
	require.NoError(t, err)
	module := moduleInterface.(*Module)

	// Test full hook workflow
	ctx := context.Background()

	// Step 1: Entrypoint hook
	entrypointResult, err := module.HandleEntrypointHook(ctx, hookstage.ModuleInvocationContext{}, getTestEntrypointPayload(t))
	require.NoError(t, err)
	assert.NotNil(t, entrypointResult.ModuleContext[asyncRequestKey])

	// Step 2: Create test request payload
	width := int64(300)
	height := int64(250)
	payload := hookstage.ProcessedAuctionRequestPayload{
		Request: &openrtb_ext.RequestWrapper{
			BidRequest: &openrtb2.BidRequest{
				ID: "test-auction",
				Imp: []openrtb2.Imp{
					{ID: "imp-1", Banner: &openrtb2.Banner{W: &width, H: &height}},
					{ID: "imp-2", Banner: &openrtb2.Banner{W: &width, H: &height}},
				},
			},
		},
	}

	// Step 3: ProcessedAuction hook
	miCtx := hookstage.ModuleInvocationContext{
		ModuleContext: entrypointResult.ModuleContext,
	}
	_, err = module.HandleProcessedAuctionHook(ctx, miCtx, payload)
	require.NoError(t, err)

	// Step 4: Create auction response with bids for both impressions
	responsePayload := hookstage.AuctionResponsePayload{
		BidResponse: &openrtb2.BidResponse{
			ID:  "test-response",
			Ext: json.RawMessage(`{}`),
			SeatBid: []openrtb2.SeatBid{
				{
					Seat: "test-seat",
					Bid: []openrtb2.Bid{
						{
							ID:    "bid-1",
							ImpID: "imp-1",
							Price: 1.0,
							Ext:   json.RawMessage(`{}`),
						},
						{
							ID:    "bid-2",
							ImpID: "imp-2",
							Price: 2.0,
							Ext:   json.RawMessage(`{}`),
						},
					},
				},
			},
		},
	}

	// Step 5: AuctionResponse hook
	responseResult, err := module.HandleAuctionResponseHook(ctx, miCtx, responsePayload)
	require.NoError(t, err)

	// Verify mutations were created
	assert.True(t, len(responseResult.ChangeSet.Mutations()) > 0)

	// Apply mutations
	modifiedPayload := responsePayload
	for _, mutation := range responseResult.ChangeSet.Mutations() {
		modifiedPayload, err = mutation.Apply(modifiedPayload)
		require.NoError(t, err)
	}

	// Verify bid-1 targeting (should have merged "cat" values)
	var bid1Ext map[string]interface{}
	err = json.Unmarshal(modifiedPayload.BidResponse.SeatBid[0].Bid[0].Ext, &bid1Ext)
	require.NoError(t, err)

	prebid1, exists := bid1Ext["prebid"].(map[string]interface{})
	require.True(t, exists)
	targeting1, exists := prebid1["targeting"].(map[string]interface{})
	require.True(t, exists)

	// Check merged category value
	assert.Equal(t, "tech,news", targeting1["cat"])
	assert.Equal(t, "positive", targeting1["sentiment"])

	// Verify bid-2 targeting (different from bid-1)
	var bid2Ext map[string]interface{}
	err = json.Unmarshal(modifiedPayload.BidResponse.SeatBid[0].Bid[1].Ext, &bid2Ext)
	require.NoError(t, err)

	prebid2, exists := bid2Ext["prebid"].(map[string]interface{})
	require.True(t, exists)
	targeting2, exists := prebid2["targeting"].(map[string]interface{})
	require.True(t, exists)

	// Check that bid-2 has different targeting
	assert.Equal(t, "sports", targeting2["cat"])
	assert.Nil(t, targeting2["sentiment"]) // Should not have sentiment
}

func TestMergeDuplicateKeys(t *testing.T) {
	kvPairs := []TargetingKeyValue{
		{Key: "category", Value: "technology"},
		{Key: "category", Value: "news"},
		{Key: "category", Value: "science"},
		{Key: "sentiment", Value: "positive"},
		{Key: "brand_safe", Value: "true"},
	}

	merged := mergeDuplicateKeys(kvPairs)

	assert.Equal(t, 3, len(merged))
	assert.Equal(t, "technology,news,science", merged["category"])
	assert.Equal(t, "positive", merged["sentiment"])
	assert.Equal(t, "true", merged["brand_safe"])
}

func TestMergeDuplicateKeys_NoDuplicates(t *testing.T) {
	kvPairs := []TargetingKeyValue{
		{Key: "key1", Value: "value1"},
		{Key: "key2", Value: "value2"},
	}

	merged := mergeDuplicateKeys(kvPairs)

	assert.Equal(t, 2, len(merged))
	assert.Equal(t, "value1", merged["key1"])
	assert.Equal(t, "value2", merged["key2"])
}

func TestAPIError(t *testing.T) {
	// Create mock server that returns an error
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("Internal Server Error"))
	}))
	defer mockServer.Close()

	// Create module with mock server
	config := json.RawMessage(`{
		"endpoint": "` + mockServer.URL + `",
		"property_id": "test-property-error",
		"timeout_ms": 1000
	}`)

	moduleInterface, err := Builder(config, getTestModuleDeps(t))
	require.NoError(t, err)
	module := moduleInterface.(*Module)

	// Test that API errors are handled gracefully
	bidRequest := &openrtb2.BidRequest{
		ID:   "test-auction",
		Site: &openrtb2.Site{Domain: "example.com"},
	}

	ctx := context.Background()
	targeting, err := module.fetchTargeting(ctx, bidRequest)
	assert.Error(t, err)
	assert.Nil(t, targeting)
	assert.Contains(t, err.Error(), "server returned status 500")
}

func TestNoTargetingForImpression(t *testing.T) {
	// Mock server returns targeting only for imp-1
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := `{
			"request_id": "test-auction",
			"targeting": {
				"imp-1": [
					{"key": "cat", "value": "tech"}
				]
			}
		}`
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(response))
	}))
	defer mockServer.Close()

	config := json.RawMessage(`{"endpoint": "` + mockServer.URL + `", "property_id": "test-property-789"}`)
	moduleInterface, err := Builder(config, getTestModuleDeps(t))
	require.NoError(t, err)
	module := moduleInterface.(*Module)

	// Full workflow
	ctx := context.Background()
	entrypointResult, _ := module.HandleEntrypointHook(ctx, hookstage.ModuleInvocationContext{}, getTestEntrypointPayload(t))

	width := int64(300)
	height := int64(250)
	payload := hookstage.ProcessedAuctionRequestPayload{
		Request: &openrtb_ext.RequestWrapper{
			BidRequest: &openrtb2.BidRequest{
				ID: "test-auction",
				Imp: []openrtb2.Imp{
					{ID: "imp-1", Banner: &openrtb2.Banner{W: &width, H: &height}},
					{ID: "imp-2", Banner: &openrtb2.Banner{W: &width, H: &height}},
				},
			},
		},
	}

	miCtx := hookstage.ModuleInvocationContext{ModuleContext: entrypointResult.ModuleContext}
	module.HandleProcessedAuctionHook(ctx, miCtx, payload)

	responsePayload := hookstage.AuctionResponsePayload{
		BidResponse: &openrtb2.BidResponse{
			ID: "test-response",
			SeatBid: []openrtb2.SeatBid{
				{
					Bid: []openrtb2.Bid{
						{ID: "bid-1", ImpID: "imp-1", Ext: json.RawMessage(`{}`)},
						{ID: "bid-2", ImpID: "imp-2", Ext: json.RawMessage(`{}`)}, // No targeting
					},
				},
			},
		},
	}

	responseResult, _ := module.HandleAuctionResponseHook(ctx, miCtx, responsePayload)
	modifiedPayload := responsePayload
	for _, mutation := range responseResult.ChangeSet.Mutations() {
		modifiedPayload, _ = mutation.Apply(modifiedPayload)
	}

	// bid-1 should have targeting
	var bid1Ext map[string]interface{}
	json.Unmarshal(modifiedPayload.BidResponse.SeatBid[0].Bid[0].Ext, &bid1Ext)
	assert.NotNil(t, bid1Ext["prebid"])

	// bid-2 should NOT have targeting (empty ext)
	var bid2Ext map[string]interface{}
	json.Unmarshal(modifiedPayload.BidResponse.SeatBid[0].Bid[1].Ext, &bid2Ext)
	// Should be empty or only have original content
	assert.Equal(t, map[string]interface{}{}, bid2Ext)
}
