package pipeline

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"path"
	"time"

	"github.com/pkg/errors"
	"go.uber.org/multierr"

	"github.com/smartcontractkit/chainlink/core/bridges"
	"github.com/smartcontractkit/chainlink/core/logger"
)

// Return types:
//
//	string
type BridgeTask struct {
	BaseTask `mapstructure:",squash"`

	Name              string `json:"name"`
	RequestData       string `json:"requestData"`
	IncludeInputAtKey string `json:"includeInputAtKey"`
	Async             string `json:"async"`
	CacheTTL          string `json:"cacheTTL"`

	specId     int32
	orm        bridges.ORM
	config     Config
	httpClient *http.Client
}

var _ Task = (*BridgeTask)(nil)

var zeroURL = new(url.URL)

func (t *BridgeTask) Type() TaskType {
	return TaskTypeBridge
}

func (t *BridgeTask) Run(ctx context.Context, lggr logger.Logger, vars Vars, inputs []Result) (result Result, runInfo RunInfo) {
	inputValues, err := CheckInputs(inputs, -1, -1, 0)
	if err != nil {
		return Result{Error: errors.Wrap(err, "task inputs")}, runInfo
	}

	var (
		name              StringParam
		requestData       MapParam
		includeInputAtKey StringParam
		cacheTTL          Uint64Param
	)
	err = multierr.Combine(
		errors.Wrap(ResolveParam(&name, From(NonemptyString(t.Name))), "name"),
		errors.Wrap(ResolveParam(&requestData, From(VarExpr(t.RequestData, vars), JSONWithVarExprs(t.RequestData, vars, false), nil)), "requestData"),
		errors.Wrap(ResolveParam(&includeInputAtKey, From(t.IncludeInputAtKey)), "includeInputAtKey"),
		errors.Wrap(ResolveParam(&cacheTTL, From(ValidDurationInSeconds(t.CacheTTL), t.config.BridgeCacheTTL().Seconds())), "cacheTTL"),
	)
	if err != nil {
		return Result{Error: err}, runInfo
	}

	url, err := t.getBridgeURLFromName(name)
	if err != nil {
		return Result{Error: err}, runInfo
	}

	var metaMap MapParam

	meta, _ := vars.Get("jobRun.meta")
	switch v := meta.(type) {
	case map[string]interface{}:
		metaMap = MapParam(v)
	case nil:
	default:
		lggr.Warnw(`"meta" field on task run is malformed, discarding`,
			"task", t.DotID(),
			"meta", meta,
		)
	}

	requestData = withRunInfo(requestData, metaMap)
	if t.IncludeInputAtKey != "" {
		if len(inputValues) > 0 {
			requestData[string(includeInputAtKey)] = inputValues[0]
		}
	}

	if t.Async == "true" {
		responseURL := t.config.BridgeResponseURL()
		if responseURL != nil && *responseURL != *zeroURL {
			responseURL.Path = path.Join(responseURL.Path, "/v2/resume/", t.uuid.String())
		}
		var s string
		if responseURL != nil {
			s = responseURL.String()
		}
		requestData["responseURL"] = s
	}

	requestDataJSON, err := json.Marshal(requestData)
	if err != nil {
		return Result{Error: err}, runInfo
	}
	lggr.Debugw("Bridge task: sending request",
		"requestData", string(requestDataJSON),
		"url", url.String(),
	)

	requestCtx, cancel := httpRequestCtx(ctx, t, t.config)
	defer cancel()

	var cachedResponse bool
	responseBytes, statusCode, headers, elapsed, err := makeHTTPRequest(requestCtx, lggr, "POST", URLParam(url), []string{}, requestData, t.httpClient, t.config.DefaultHTTPLimit())
	if err != nil {
		if cacheTTL == 0 {
			return Result{Error: err}, RunInfo{IsRetryable: isRetryableHTTPError(statusCode, err)}
		}

		var cacheErr error
		lggr.Debugw("Bridge task: task failed, looking up cached value",
			"err", err,
			"cacheTTL in seconds", cacheTTL,
			"ttl", time.Duration(cacheTTL)*time.Second,
			"elapsed", time.Now().Add(-time.Duration(cacheTTL)*time.Second),
		)
		responseBytes, cacheErr = t.orm.GetCachedResponse(t.dotID, t.specId, time.Duration(cacheTTL)*time.Second)
		if cacheErr != nil {
			lggr.Errorw("Bridge task: cache fallback failed",
				"err", cacheErr.Error(),
				"url", url.String(),
			)
			return Result{Error: err}, RunInfo{IsRetryable: isRetryableHTTPError(statusCode, err)}
		}
		lggr.Debugw("Bridge task: request failed, falling back to cache",
			"response", string(responseBytes),
			"url", url.String(),
		)
		cachedResponse = true
	}

	if t.Async == "true" {
		// Look for a `pending` flag. This check is case-insensitive because http.Header normalizes header names
		if _, ok := headers["X-Chainlink-Pending"]; ok {
			return result, pendingRunInfo()
		}

		var response struct {
			Pending bool `json:"pending"`
		}
		if err := json.Unmarshal(responseBytes, &response); err == nil && response.Pending {
			return Result{}, pendingRunInfo()
		}
	}

	if !cachedResponse && cacheTTL > 0 {
		err := t.orm.UpsertBridgeResponse(t.dotID, t.specId, responseBytes)
		if err != nil {
			lggr.Errorw("Bridge task: failed to upsert response in bridge cache", "err", err)
		}
	}

	// NOTE: We always stringify the response since this is required for all current jobs.
	// If a binary response is required we might consider adding an adapter
	// flag such as  "BinaryMode: true" which passes through raw binary as the
	// value instead.
	result = Result{Value: string(responseBytes)}

	promHTTPFetchTime.WithLabelValues(t.DotID()).Set(float64(elapsed))
	promHTTPResponseBodySize.WithLabelValues(t.DotID()).Set(float64(len(responseBytes)))

	lggr.Debugw("Bridge task: fetched answer",
		"answer", result.Value,
		"url", url.String(),
		"dotID", t.DotID(),
		"cached", cachedResponse,
	)
	return result, runInfo
}

func (t BridgeTask) getBridgeURLFromName(name StringParam) (URLParam, error) {
	bt, err := t.orm.FindBridge(bridges.BridgeName(name))
	if err != nil {
		return URLParam{}, errors.Wrapf(err, "could not find bridge with name '%s'", name)
	}
	return URLParam(bt.URL), nil
}

func withRunInfo(request MapParam, meta MapParam) MapParam {
	output := make(MapParam)
	for k, v := range request {
		output[k] = v
	}
	if meta != nil {
		output["meta"] = meta
	}
	return output
}
