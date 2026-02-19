# SigNoz Trace Query Guide

- Instance: `$SIGNOZ_URL` (set via environment variable)
- Auth: `SIGNOZ-API-KEY` header (env: `$SIGNOZ_API_KEY`)
- API: `POST $SIGNOZ_URL/api/v3/query_range`

## Quick Reference

- Services: `weka-operator`, `weka-operator-runtime`
- Key spans: `WekaContainerReconcile`, `WekaClusterReconcile`, `ReconciliationSteps`,
  `weka.jsonrpc_call`, `metrics.handler`, `http`
- Auth header: `-H "SIGNOZ-API-KEY: $SIGNOZ_API_KEY"`
- Time: start/end in epoch milliseconds

## Builder Query Template

All queries use this envelope. Change filters/selectColumns/orderBy as needed.

```json
{
  "start": <epoch_ms>,
  "end": <epoch_ms>,
  "compositeQuery": {
    "queryType": "builder",
    "panelType": "list",
    "builderQueries": {
      "A": {
        "dataSource": "traces",
        "queryName": "A",
        "expression": "A",
        "aggregateOperator": "noop",
        "aggregateAttribute": {"key":"","dataType":"","type":"","isColumn":false},
        "filters": { "items": [...], "op": "AND" },
        "selectColumns": [...],
        "orderBy": [{"columnName":"timestamp","order":"desc"}],
        "limit": 20,
        "offset": 0
      }
    }
  }
}
```

### Filter item format

```json
{"key":{"key":"<field>","dataType":"<type>","type":"<tag|resource>","isColumn":true},"op":"<op>","value":"<val>"}
```

### Common fields (isColumn: true)

| Field | Type | DataType |
|-------|------|----------|
| serviceName | resource | string |
| name | tag | string |
| traceID | tag | string |
| hasError | tag | bool |
| durationNano | tag | string |
| parentSpanID | tag | string |
| httpMethod | tag | string |

Operators: `=`, `!=`, `>`, `<`, `IN`, `NOT IN`, `CONTAINS`, `REGEXP`, `EXISTS`

## Shell Helpers

```bash
# Last 5 minutes
START=$(($(date +%s)*1000 - 300000)); END=$(($(date +%s)*1000))
# Last hour
START=$(($(date +%s)*1000 - 3600000)); END=$(($(date +%s)*1000))
# Last day
START=$(($(date +%s)*1000 - 86400000)); END=$(($(date +%s)*1000))
```

## Query: Recent operator traces

```bash
START=$(($(date +%s)*1000 - 3600000)); END=$(($(date +%s)*1000))
curl -s -X POST "$SIGNOZ_URL/api/v3/query_range" \
  -H "Content-Type: application/json" \
  -H "SIGNOZ-API-KEY: $SIGNOZ_API_KEY" \
  -d "{
    \"start\":$START,\"end\":$END,
    \"compositeQuery\":{
      \"queryType\":\"builder\",\"panelType\":\"list\",
      \"builderQueries\":{\"A\":{
        \"dataSource\":\"traces\",\"queryName\":\"A\",\"expression\":\"A\",
        \"aggregateOperator\":\"noop\",
        \"aggregateAttribute\":{\"key\":\"\",\"dataType\":\"\",\"type\":\"\",\"isColumn\":false},
        \"filters\":{\"items\":[
          {\"key\":{\"key\":\"serviceName\",\"dataType\":\"string\",\"type\":\"resource\",\"isColumn\":true},\"op\":\"=\",\"value\":\"weka-operator\"}
        ],\"op\":\"AND\"},
        \"selectColumns\":[
          {\"key\":\"serviceName\",\"dataType\":\"string\",\"type\":\"resource\",\"isColumn\":true},
          {\"key\":\"name\",\"dataType\":\"string\",\"type\":\"tag\",\"isColumn\":true},
          {\"key\":\"durationNano\",\"dataType\":\"string\",\"type\":\"tag\",\"isColumn\":true},
          {\"key\":\"hasError\",\"dataType\":\"bool\",\"type\":\"tag\",\"isColumn\":true}
        ],
        \"orderBy\":[{\"columnName\":\"timestamp\",\"order\":\"desc\"}],
        \"limit\":20,\"offset\":0
      }}
    }
  }" | jq '.data.result[0].list[] | {name:.data.name, duration_ms:(.data.durationNano/1000000|round), hasError:.data.hasError, traceID:.data.traceID}'
```

## Query: Trace by ID

Replace the serviceName filter with:
```json
{"key":{"key":"traceID","dataType":"string","type":"tag","isColumn":true},"op":"=","value":"TRACE_ID"}
```
Change orderBy to `[{"columnName":"timestamp","order":"asc"}]`, set limit to 100.

## Query: Reconciliation spans only

Add to filters.items:
```json
{"key":{"key":"name","dataType":"string","type":"tag","isColumn":true},"op":"CONTAINS","value":"Reconcile"}
```

## Query: Error spans

Add to filters.items:
```json
{"key":{"key":"hasError","dataType":"bool","type":"tag","isColumn":true},"op":"=","value":"true"}
```

## Query: Root spans only

Add to filters.items:
```json
{"key":{"key":"parentSpanID","dataType":"string","type":"tag","isColumn":true},"op":"=","value":""}
```

## Query: Span by operation name

Useful span names: `WekaContainerReconcile`, `WekaClusterReconcile`,
`WekaClusterReconcileLoop`, `ReconciliationSteps`, `ClientReconcile`,
`weka.jsonrpc_call`, `metrics.handler`, `metrics.generate`, `http`

Add to filters.items:
```json
{"key":{"key":"name","dataType":"string","type":"tag","isColumn":true},"op":"=","value":"WekaContainerReconcile"}
```

## Query: Including custom span attributes

Custom span attributes (like `mode`, `container`, etc.) use `isColumn: false` in selectColumns:
```json
{"key":"mode","dataType":"string","type":"tag","isColumn":false}
```

## Recipe: Count spans per minute per mode

Fetches WekaContainerReconcile spans from the last 5 minutes and aggregates per minute per mode using jq:

```bash
START=$(($(date +%s)*1000 - 300000)); END=$(($(date +%s)*1000))
curl -s -X POST "$SIGNOZ_URL/api/v3/query_range" \
  -H "Content-Type: application/json" \
  -H "SIGNOZ-API-KEY: $SIGNOZ_API_KEY" \
  -d "{
    \"start\":$START,\"end\":$END,
    \"compositeQuery\":{
      \"queryType\":\"builder\",\"panelType\":\"list\",
      \"builderQueries\":{\"A\":{
        \"dataSource\":\"traces\",\"queryName\":\"A\",\"expression\":\"A\",
        \"aggregateOperator\":\"noop\",
        \"aggregateAttribute\":{\"key\":\"\",\"dataType\":\"\",\"type\":\"\",\"isColumn\":false},
        \"filters\":{\"items\":[
          {\"key\":{\"key\":\"serviceName\",\"dataType\":\"string\",\"type\":\"resource\",\"isColumn\":true},\"op\":\"=\",\"value\":\"weka-operator\"},
          {\"key\":{\"key\":\"name\",\"dataType\":\"string\",\"type\":\"tag\",\"isColumn\":true},\"op\":\"=\",\"value\":\"WekaContainerReconcile\"}
        ],\"op\":\"AND\"},
        \"selectColumns\":[
          {\"key\":\"mode\",\"dataType\":\"string\",\"type\":\"tag\",\"isColumn\":false}
        ],
        \"orderBy\":[{\"columnName\":\"timestamp\",\"order\":\"asc\"}],
        \"limit\":10000,\"offset\":0
      }}
    }
  }" | jq -r '
    [.data.result[0].list[] | {
      minute: (.timestamp | split(":")[0:2] | join(":")),
      mode: .data.mode
    }] | group_by(.minute) | .[] |
    . as $grp |
    ($grp[0].minute) as $min |
    ($grp | group_by(.mode) | map({mode: .[0].mode, count: length}) | sort_by(.mode)) as $modes |
    "\($min) | \($modes | map("\(.mode): \(.count)") | join(", "))"
  '
```

Adapt by changing: the span `name` filter, the time range, the `selectColumns` attribute, and the jq grouping field.

## ClickHouse SQL Query

> **WARNING**: ClickHouse SQL queries may silently return empty results depending on SigNoz deployment. Always prefer builder queries. Use ClickHouse SQL only if builder queries are insufficient and you've verified it works.

For advanced queries. Use `$start_timestamp`, `$end_timestamp`, `$start_datetime`, `$end_datetime` as template variables. Always include `ts_bucket_start` filter for performance.

```json
{
  "start": <epoch_ms>,
  "end": <epoch_ms>,
  "compositeQuery": {
    "queryType": "clickhouse_sql",
    "panelType": "list",
    "builderQueries": {},
    "clickhouseQueries": {
      "A": {
        "name": "A",
        "disabled": false,
        "query": "SELECT name, count() as cnt FROM signoz_traces.distributed_signoz_index_v3 WHERE resource_string_service$$name = 'weka-operator' AND ts_bucket_start >= toUnixTimestamp(now() - INTERVAL 1 DAY) GROUP BY name ORDER BY cnt DESC LIMIT 30"
      }
    }
  }
}
```

### ClickHouse schema reference

- Table: `signoz_traces.distributed_signoz_index_v3`
- Key columns: `timestamp`, `trace_id`, `span_id`, `parent_span_id`, `name`, `duration_nano`, `has_error`, `resource_string_service$$name`
- `ts_bucket_start`: UInt64, 30min bucket, part of primary key - always filter on it

## Response Format

All queries return:
```json
{
  "status": "success",
  "data": {
    "result": [{
      "queryName": "A",
      "list": [{
        "timestamp": "2026-02-19T11:15:43Z",
        "data": {
          "name": "WekaContainerReconcile",
          "serviceName": "weka-operator",
          "durationNano": 168000000,
          "hasError": false,
          "spanID": "4e0f6ee62bd00ee6",
          "traceID": "5691bd3d6da1e1659204fc2f46256572"
        }
      }]
    }]
  }
}
```

Parse with: `jq '.data.result[0].list[]'`
