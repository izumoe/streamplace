---
title: place.stream.bio.blocks.image
description: Reference for the place.stream.bio.blocks.image lexicon
---

**Lexicon Version:** 1

## Definitions

<a name="main"></a>

### `main`

**Type:** `object`

**Properties:**

| Name          | Type                           | Req'd | Description                                              | Constraints                                   |
| ------------- | ------------------------------ | ----- | -------------------------------------------------------- | --------------------------------------------- |
| `image`       | `blob`                         | ✅    |                                                          | Accept: `image/*`<br/>Max Size: 5000000 bytes |
| `aspectRatio` | [`#aspectRatio`](#aspectratio) | ✅    |                                                          |                                               |
| `alt`         | `string`                       | ❌    | Alt text describing the image, for accessibility.        |                                               |
| `url`         | `string`                       | ❌    | Optional destination to open when the image is selected. | Format: `uri`                                 |

---

<a name="aspectratio"></a>

### `aspectRatio`

**Type:** `object`

**Properties:**

| Name     | Type      | Req'd | Description | Constraints |
| -------- | --------- | ----- | ----------- | ----------- |
| `width`  | `integer` | ✅    |             |             |
| `height` | `integer` | ✅    |             |             |

---

## Lexicon Source

```json
{
  "lexicon": 1,
  "id": "place.stream.bio.blocks.image",
  "defs": {
    "main": {
      "type": "object",
      "required": ["image", "aspectRatio"],
      "properties": {
        "image": {
          "type": "blob",
          "accept": ["image/*"],
          "maxSize": 5000000
        },
        "aspectRatio": {
          "type": "ref",
          "ref": "#aspectRatio"
        },
        "alt": {
          "type": "string",
          "description": "Alt text describing the image, for accessibility."
        },
        "url": {
          "type": "string",
          "format": "uri",
          "description": "Optional destination to open when the image is selected."
        }
      }
    },
    "aspectRatio": {
      "type": "object",
      "required": ["width", "height"],
      "properties": {
        "width": {
          "type": "integer"
        },
        "height": {
          "type": "integer"
        }
      }
    }
  }
}
```
