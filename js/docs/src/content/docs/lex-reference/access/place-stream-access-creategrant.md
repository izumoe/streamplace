---
title: place.stream.access.createGrant
description: Reference for the place.stream.access.createGrant lexicon
---

**Lexicon Version:** 1

## Definitions

<a name="main"></a>

### `main`

**Type:** `procedure`

Create an allowance or denial for local publishing or chat. Repeating an identical entry is idempotent; changing its metadata requires revoking that exact entry first. Requires node administrator authorization.

**Parameters:** _(None defined)_

**Input:**

- **Encoding:** `application/json`
- **Schema:**

**Schema Type:** `object`

| Name         | Type     | Req'd | Description | Constraints              |
| ------------ | -------- | ----- | ----------- | ------------------------ |
| `subject`    | `string` | ✅    |             | Format: `did`            |
| `capability` | `string` | ✅    |             | Enum: `streamer`, `chat` |
| `effect`     | `string` | ✅    |             | Enum: `allow`, `deny`    |
| `reason`     | `string` | ❌    |             | Max Length: 1024         |
| `expiresAt`  | `string` | ❌    |             | Format: `datetime`       |

**Output:**

- **Encoding:** `application/json`
- **Schema:**

**Schema Type:** [`place.stream.access.defs#grant`](/lex-reference/place-stream-access-defs#grant)

**Possible Errors:**

- `Unauthorized`
- `Forbidden`
- `InvalidRequest`
- `Conflict`
- `Unavailable`

---

## Lexicon Source

```json
{
  "lexicon": 1,
  "id": "place.stream.access.createGrant",
  "defs": {
    "main": {
      "type": "procedure",
      "description": "Create an allowance or denial for local publishing or chat. Repeating an identical entry is idempotent; changing its metadata requires revoking that exact entry first. Requires node administrator authorization.",
      "input": {
        "encoding": "application/json",
        "schema": {
          "type": "object",
          "required": ["subject", "capability", "effect"],
          "properties": {
            "subject": {
              "type": "string",
              "format": "did"
            },
            "capability": {
              "type": "string",
              "enum": ["streamer", "chat"]
            },
            "effect": {
              "type": "string",
              "enum": ["allow", "deny"]
            },
            "reason": {
              "type": "string",
              "maxLength": 1024
            },
            "expiresAt": {
              "type": "string",
              "format": "datetime"
            }
          }
        }
      },
      "output": {
        "encoding": "application/json",
        "schema": {
          "type": "ref",
          "ref": "place.stream.access.defs#grant"
        }
      },
      "errors": [
        {
          "name": "Unauthorized"
        },
        {
          "name": "Forbidden"
        },
        {
          "name": "InvalidRequest"
        },
        {
          "name": "Conflict"
        },
        {
          "name": "Unavailable"
        }
      ]
    }
  }
}
```
