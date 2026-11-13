---
title: place.stream.access.deleteGrant
description: Reference for the place.stream.access.deleteGrant lexicon
---

**Lexicon Version:** 1

## Definitions

<a name="main"></a>

### `main`

**Type:** `procedure`

Revoke one stored node-local entry by its exact ID. Does not remove another allowance or denial for the same DID. Requires node administrator authorization.

**Parameters:** _(None defined)_

**Input:**

- **Encoding:** `application/json`
- **Schema:**

**Schema Type:** `object`

| Name | Type     | Req'd | Description | Constraints                       |
| ---- | -------- | ----- | ----------- | --------------------------------- |
| `id` | `string` | ✅    |             | Min Length: 1<br/>Max Length: 256 |

**Output:**

- **Encoding:** `application/json`
- **Schema:**

**Schema Type:** `object`

| Name      | Type      | Req'd | Description | Constraints |
| --------- | --------- | ----- | ----------- | ----------- |
| `success` | `boolean` | ✅    |             |             |

**Possible Errors:**

- `Unauthorized`
- `Forbidden`
- `NotFound`
- `InvalidRequest`
- `Unavailable`

---

## Lexicon Source

```json
{
  "lexicon": 1,
  "id": "place.stream.access.deleteGrant",
  "defs": {
    "main": {
      "type": "procedure",
      "description": "Revoke one stored node-local entry by its exact ID. Does not remove another allowance or denial for the same DID. Requires node administrator authorization.",
      "input": {
        "encoding": "application/json",
        "schema": {
          "type": "object",
          "required": ["id"],
          "properties": {
            "id": {
              "type": "string",
              "minLength": 1,
              "maxLength": 256
            }
          }
        }
      },
      "output": {
        "encoding": "application/json",
        "schema": {
          "type": "object",
          "required": ["success"],
          "properties": {
            "success": {
              "type": "boolean"
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
          "name": "NotFound"
        },
        {
          "name": "InvalidRequest"
        },
        {
          "name": "Unavailable"
        }
      ]
    }
  }
}
```
