# Nodals AI RTD Module

This module integrates Nodals AI's Real-Time Data API to analyze first-party signals and provide dynamic targeting key-values that indicate high-value ad opportunities.

## Overview

The Nodals AI RTD module analyzes contextual and audience signals during the auction to determine their value to advertisers. The module makes a real-time call to the Nodals API with the bid request data and receives targeting key-value pairs that are then added to the auction response.

For account setup and API access, contact: info@nodals.ai

## Configuration

### YAML Configuration
```yaml
hooks:
  enabled: true
  modules:
    nodals:
      rtd:
        enabled: true
        endpoint: "https://s2s.nodals.io/s/v1/select"
        property_id: "e6b50d21"  # Required: Your Nodals property identifier
        timeout_ms: 200  # Optional: HTTP timeout in milliseconds (default: 500)
  host_execution_plan:
    endpoints:
      /openrtb2/auction:
        stages:
          entrypoint:
            groups:
              - timeout: 5
                hook_sequence:
                  - module_code: "nodals.rtd"
                    hook_impl_code: "HandleEntrypointHook"
          processed_auction_request:
            groups:
              - timeout: 2000
                hook_sequence:
                  - module_code: "nodals.rtd"
                    hook_impl_code: "HandleProcessedAuctionHook"
          auction_response:
            groups:
              - timeout: 5
                hook_sequence:
                  - module_code: "nodals.rtd"
                    hook_impl_code: "HandleAuctionResponseHook"
```

### JSON Configuration
```json
{
  "hooks": {
    "enabled": true,
    "modules": {
      "nodals": {
        "rtd": {
          "enabled": true,
          "endpoint": "https://s2s.nodals.io/s/v1/select",
          "property_id": "e6b50d21",
          "timeout_ms": 200
        }
      }
    },
    "host_execution_plan": {
      "endpoints": {
        "/openrtb2/auction": {
          "stages": {
            "entrypoint": {
              "groups": [
                {
                  "timeout": 5,
                  "hook_sequence": [
                    {
                      "module_code": "nodals.rtd",
                      "hook_impl_code": "HandleEntrypointHook"
                    }
                  ]
                }
              ]
            },
            "processed_auction_request": {
              "groups": [
                {
                  "timeout": 2000,
                  "hook_sequence": [
                    {
                      "module_code": "nodals.rtd",
                      "hook_impl_code": "HandleProcessedAuctionHook"
                    }
                  ]
                }
              ]
            },
            "auction_response": {
              "groups": [
                {
                  "timeout": 5,
                  "hook_sequence": [
                    {
                      "module_code": "nodals.rtd",
                      "hook_impl_code": "HandleAuctionResponseHook"
                    }
                  ]
                }
              ]
            }
          }
        }
      }
    }
  }
}
```

## Configuration Parameters

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `endpoint` | string | Yes | - | URL of the Nodals selection API |
| `property_id` | string | Yes | - | Your Nodals property identifier (provided during account setup) |
| `timeout_ms` | integer | No | 500 | HTTP request timeout in milliseconds |

## How It Works

1. **Entrypoint Hook**: Initializes an asynchronous request context for the auction
2. **Processed Auction Request Hook**: Sends the OpenRTB bid request to the Nodals API asynchronously
3. **Auction Response Hook**: Waits for the Nodals API response and applies targeting key-values to bids

### API Request Format

The module sends the complete OpenRTB bid request to the configured endpoint with the `property_id` automatically injected into the request at `site.ext.nodals.pid`. This allows the Nodals API to identify which property the request is for and apply the appropriate targeting logic.

### API Response Format

The Nodals API should return targeting data in the following format:

```json
{
  "request_id": "bid-request-id",
  "targeting": {
    "impression-id-1": [
      {"key": "nodals_value", "value": "high"},
      {"key": "nodals_category", "value": "premium"}
    ],
    "impression-id-2": [
      {"key": "nodals_value", "value": "medium"}
    ]
  }
}
```

- `request_id`: The OpenRTB bid request ID (for tracking/debugging)
- `targeting`: Object mapping impression IDs to arrays of key-value pairs

## Data Output

### Targeting Integration

The module adds targeting key-values directly to each bid's `ext.prebid.targeting` object. These targeting keys are automatically available to:

- **Google Ad Manager (GAM)**: Passed as key-values when using Prebid's GAM integration
- **Other Ad Servers**: Available for custom targeting implementations
- **Analytics**: Accessible in bid response analytics

### Response Format

Targeting data is added to individual bids based on impression ID matching:

```json
{
  "seatbid": [
    {
      "bid": [
        {
          "id": "bid-123",
          "impid": "impression-id-1",
          "price": 2.50,
          "ext": {
            "prebid": {
              "targeting": {
                "hb_bidder": "appnexus",
                "hb_pb": "2.50",
                "nodals_value": "high",
                "nodals_category": "premium"
              }
            }
          }
        }
      ]
    }
  ]
}
```

### Key Merging

If the Nodals API returns multiple values for the same key, they are automatically merged into a comma-separated string:

**API Response:**
```json
{
  "targeting": {
    "imp-1": [
      {"key": "category", "value": "tech"},
      {"key": "category", "value": "news"}
    ]
  }
}
```

**Result in Bid:**
```json
{
  "ext": {
    "prebid": {
      "targeting": {
        "category": "tech,news"
      }
    }
  }
}
```

## Features

- **Per-Impression Targeting**: Different targeting values for each ad unit based on context
- **Dynamic Key-Values**: Supports any key-value pairs returned by the Nodals API
- **Asynchronous Processing**: Non-blocking API calls to minimize auction latency
- **Graceful Degradation**: Auction continues normally if Nodals API is unavailable
- **Automatic Key Merging**: Handles duplicate keys with comma-separated values
- **Error Reporting**: Detailed analytics tags for monitoring and debugging

## Performance Considerations

- **Async Pattern**: The module uses Go routines to fetch targeting data without blocking the auction
- **Configurable Timeout**: Set `timeout_ms` to balance between data freshness and auction speed
- **No Caching**: Each auction makes a fresh API call to ensure real-time targeting
- **Memory Efficient**: Minimal memory overhead with context-based request management

## Error Handling

The module is designed to fail gracefully:

- API errors are logged but do not fail the auction
- Network timeouts return empty targeting (auction proceeds normally)
- Malformed responses are caught and logged via analytics
- Missing impression IDs are safely ignored

All errors are reported through Prebid Server's analytics system for monitoring.


## Privacy & Compliance

The module forwards the complete OpenRTB bid request to the Nodals API. Publishers are responsible for:

- Ensuring appropriate user consent is obtained before enabling the module
- Configuring the module in compliance with applicable privacy regulations (GDPR, CCPA, etc.)
- Understanding what data is being sent to the Nodals API

The module does not perform any data masking or filtering. Publishers should work with Nodals AI to understand data handling practices and ensure compliance with privacy requirements.
