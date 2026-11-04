---
title: place.stream.access.listGrants
description: Reference for the place.stream.access.listGrants lexicon
---

**Lexicon Version:** 1

## Definitions

<a name="main"></a>

### `main`

**Type:** `query`

List node-local capability entries, including immutable configuration entries. Requires node administrator authorization.

**Parameters:** _(None defined)_

**Output:**

- **Encoding:** `application/json`
- **Schema:**

**Schema Type:** `object`

| Name       | Type                                                                                       | Req'd | Description | Constraints |
| ---------- | ------------------------------------------------------------------------------------------ | ----- | ----------- | ----------- |
| `revision` | `integer`                                                                                  | ✅    |             | Min: 0      |
| `grants`   | Array of [`place.stream.access.defs#grant`](/lex-reference/place-stream-access-defs#grant) | ✅    |             |             |

**Possible Errors:**

- `Unauthorized`
- `Forbidden`
- `Unavailable`

---

## Lexicon Source

```json
{
  "lexicon": 1,
  "id": "place.stream.access.listGrants",
  "defs": {
    "main": {
      "type": "query",
      "description": "List node-local capability entries, including immutable configuration entries. Requires node administrator authorization.",
      "output": {
        "encoding": "application/json",
        "schema": {
          "type": "object",
          "required": ["revision", "grants"],
          "properties": {
            "revision": {
              "type": "integer",
              "minimum": 0
            },
            "grants": {
              "type": "array",
              "items": {
                "type": "ref",
                "ref": "place.stream.access.defs#grant"
              }
            }
          }
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
          "name": "Unavailable"
        }
      ]
    }
  }
}
```
