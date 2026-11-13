import { expect, it } from "vitest";
import { brandingLinks, isWebUrl } from "./branding";

it("only exposes http(s) branding links with readable labels", () => {
  expect(
    brandingLinks(
      JSON.stringify([
        { label: "Community", url: "https://community.example" },
        { text: "Privacy", url: "https://node.example/privacy" },
        { label: "Script", url: "javascript:alert(1)" },
        { label: "", url: "https://empty.example" },
        { label: 7, url: "https://invalid.example" },
        { label: "Data", url: "data:text/html,test" },
        null,
      ]),
    ),
  ).toEqual([
    { label: "Community", url: "https://community.example" },
    { label: "Privacy", url: "https://node.example/privacy" },
  ]);
  for (const value of [
    "//elsewhere.example",
    "/privacy",
    "javascript:alert(1)",
    "not a URL",
  ])
    expect(isWebUrl(value)).toBe(false);
  expect(isWebUrl("http://localhost:8080/privacy")).toBe(true);
});
it("ignores malformed legacy link data without breaking navigation", () => {
  for (const value of [undefined, "null", "{}", "not JSON", "[1]"])
    expect(brandingLinks(value)).toEqual([]);
});
