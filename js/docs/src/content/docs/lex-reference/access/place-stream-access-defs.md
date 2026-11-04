---
title: place.stream.access.defs
description: Reference for the place.stream.access.defs lexicon
---

**Lexicon Version:** 1

## Definitions

<a name="grant"></a>

### `grant`

**Type:** `object`

An exact node-local capability entry. Configuration entries cannot be revoked through this API.

**Properties:**

| Name               | Type      | Req'd | Description                                                                     | Constraints                                                     |
| ------------------ | --------- | ----- | ------------------------------------------------------------------------------- | --------------------------------------------------------------- |
| `id`               | `string`  | ✅    |                                                                                 |                                                                 |
| `authority`        | `string`  | ✅    |                                                                                 | Format: `did`                                                   |
| `subject`          | `string`  | ✅    |                                                                                 | Format: `did`                                                   |
| `capability`       | `string`  | ✅    |                                                                                 | Enum: `admin`, `viewer`, `chat`, `streamer`, `syndicate`, `vod` |
| `effect`           | `string`  | ✅    |                                                                                 | Enum: `allow`, `deny`                                           |
| `reason`           | `string`  | ✅    |                                                                                 |                                                                 |
| `source`           | `string`  | ✅    |                                                                                 | Known Values: `configuration`, `stored`                         |
| `createdAt`        | `string`  | ❌    |                                                                                 | Format: `datetime`                                              |
| `expiresAt`        | `string`  | ❌    |                                                                                 | Format: `datetime`                                              |
| `effectiveAllowed` | `boolean` | ✅    | The subject’s current effective decision, which may be denied by another entry. |                                                                 |

---

<a name="decision"></a>

### `decision`

**Type:** `object`

**Properties:**

| Name         | Type      | Req'd | Description                                                                                                        | Constraints                                                     |
| ------------ | --------- | ----- | ------------------------------------------------------------------------------------------------------------------ | --------------------------------------------------------------- |
| `capability` | `string`  | ✅    |                                                                                                                    | Enum: `admin`, `viewer`, `chat`, `streamer`, `syndicate`, `vod` |
| `mode`       | `string`  | ✅    |                                                                                                                    | Enum: `open`, `allowlist`, `off`                                |
| `allowed`    | `boolean` | ✅    |                                                                                                                    |                                                                 |
| `reason`     | `string`  | ✅    |                                                                                                                    |                                                                 |
| `enforced`   | `boolean` | ✅    | Whether this node implements this capability gate. Informational defaults do not imply restricted viewing support. |                                                                 |

---

## Lexicon Source

```json
{
  "lexicon": 1,
  "id": "place.stream.access.defs",
  "defs": {
    "grant": {
      "type": "object",
      "description": "An exact node-local capability entry. Configuration entries cannot be revoked through this API.",
      "required": [
        "id",
        "authority",
        "subject",
        "capability",
        "effect",
        "reason",
        "source",
        "effectiveAllowed"
      ],
      "properties": {
        "id": {
          "type": "string"
        },
        "authority": {
          "type": "string",
          "format": "did"
        },
        "subject": {
          "type": "string",
          "format": "did"
        },
        "capability": {
          "type": "string",
          "enum": ["admin", "viewer", "chat", "streamer", "syndicate", "vod"]
        },
        "effect": {
          "type": "string",
          "enum": ["allow", "deny"]
        },
        "reason": {
          "type": "string"
        },
        "source": {
          "type": "string",
          "knownValues": ["configuration", "stored"]
        },
        "createdAt": {
          "type": "string",
          "format": "datetime"
        },
        "expiresAt": {
          "type": "string",
          "format": "datetime"
        },
        "effectiveAllowed": {
          "type": "boolean",
          "description": "The subject’s current effective decision, which may be denied by another entry."
        }
      }
    },
    "decision": {
      "type": "object",
      "required": ["capability", "mode", "allowed", "reason", "enforced"],
      "properties": {
        "capability": {
          "type": "string",
          "enum": ["admin", "viewer", "chat", "streamer", "syndicate", "vod"]
        },
        "mode": {
          "type": "string",
          "enum": ["open", "allowlist", "off"]
        },
        "allowed": {
          "type": "boolean"
        },
        "reason": {
          "type": "string"
        },
        "enforced": {
          "type": "boolean",
          "description": "Whether this node implements this capability gate. Informational defaults do not imply restricted viewing support."
        }
      }
    }
  }
}
```
