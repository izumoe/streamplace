---
title: place.stream.access.getStatus
description: Reference for the place.stream.access.getStatus lexicon
---

**Lexicon Version:** 1

## Definitions

<a name="main"></a>

### `main`

**Type:** `query`

Get effective capabilities on this node for the caller. Only administrators may inspect another DID. Anonymous callers can inspect their own public defaults.

**Parameters:**

| Name      | Type     | Req'd | Description | Constraints   |
| --------- | -------- | ----- | ----------- | ------------- |
| `subject` | `string` | ❌    |             | Format: `did` |

**Output:**

- **Encoding:** `application/json`
- **Schema:**

**Schema Type:** `object`

| Name           | Type                                                                                             | Req'd | Description | Constraints   |
| -------------- | ------------------------------------------------------------------------------------------------ | ----- | ----------- | ------------- |
| `revision`     | `integer`                                                                                        | ✅    |             | Min: 0        |
| `subject`      | `string`                                                                                         | ❌    |             | Format: `did` |
| `capabilities` | Array of [`place.stream.access.defs#decision`](/lex-reference/place-stream-access-defs#decision) | ✅    |             |               |

**Possible Errors:**

- `Forbidden`
- `Unavailable`

---

## Lexicon Source

```json
{
  "lexicon": 1,
  "id": "place.stream.access.getStatus",
  "defs": {
    "main": {
      "type": "query",
      "description": "Get effective capabilities on this node for the caller. Only administrators may inspect another DID. Anonymous callers can inspect their own public defaults.",
      "parameters": {
        "type": "params",
        "properties": {
          "subject": {
            "type": "string",
            "format": "did"
          }
        }
      },
      "output": {
        "encoding": "application/json",
        "schema": {
          "type": "object",
          "required": ["revision", "capabilities"],
          "properties": {
            "revision": {
              "type": "integer",
              "minimum": 0
            },
            "subject": {
              "type": "string",
              "format": "did"
            },
            "capabilities": {
              "type": "array",
              "items": {
                "type": "ref",
                "ref": "place.stream.access.defs#decision"
              }
            }
          }
        }
      },
      "errors": [
        {
          "name": "Forbidden"
        },
        {
          "name": "Unavailable"
        }
      ]
    }
  }
}
```
