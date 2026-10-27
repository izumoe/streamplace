import { createServer, type Server } from "node:http";
import { act, createElement } from "react";
import { createRoot } from "react-dom/client";
import { place, StreamplaceAgent } from "streamplace";
import {
  afterEach,
  beforeAll,
  beforeEach,
  describe,
  expect,
  it,
  vi,
} from "vitest";
import { humanBytes, useUpload, validateVideoFile } from "../hooks/use-upload";

const { importWarning } = vi.hoisted(() => ({
  importWarning: vi.spyOn(console, "warn").mockImplementation(() => {}),
}));
let agent: StreamplaceAgent;
vi.mock("../lib/store/hooks", () => ({ usePDSAgent: () => agent }));
// Vitest otherwise resolves tus's Node entry, which cannot upload browser Files.
vi.mock("tus-js-client", () =>
  vi.importActual("tus-js-client/lib.es5/browser/index.js"),
);
vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

beforeAll(() => {
  try {
    expect(importWarning.mock.calls).toEqual([
      [
        "[@atproto/lex-data]: Uint8Array.fromBase64 / Uint8Array.prototype.toBase64 not available in this environment. Falling back to ponyfill implementation.",
      ],
    ]);
  } finally {
    importWarning.mockRestore();
  }
});

describe("humanBytes", () => {
  it("formats bytes", () => {
    expect(humanBytes(0)).toBe("0 B");
    expect(humanBytes(512)).toBe("512 B");
    expect(humanBytes(1023)).toBe("1023 B");
  });

  it("formats kilobytes", () => {
    expect(humanBytes(1024)).toBe("1.0 KB");
    expect(humanBytes(1536)).toBe("1.5 KB");
    expect(humanBytes(1048575)).toBe("1024.0 KB");
  });

  it("formats megabytes", () => {
    expect(humanBytes(1048576)).toBe("1.0 MB");
    expect(humanBytes(5242880)).toBe("5.0 MB");
  });

  it("formats gigabytes", () => {
    expect(humanBytes(1073741824)).toBe("1.00 GB");
    expect(humanBytes(5368709120)).toBe("5.00 GB");
  });
});

describe("validateVideoFile", () => {
  async function makeFile(bytes: number[], type = "video/mp4"): Promise<File> {
    const buf = new Uint8Array(bytes);
    return new File([buf], "test", { type });
  }

  it("accepts MP4 (ftyp box at offset 4)", async () => {
    // 4 zero bytes + "ftyp" + remaining
    const file = await makeFile([
      0, 0, 0, 0, 0x66, 0x74, 0x79, 0x70, 0, 0, 0, 0,
    ]);
    expect(await validateVideoFile(file)).toBeNull();
  });

  it("accepts WebM/MKV (EBML magic)", async () => {
    const file = await makeFile([
      0x1a, 0x45, 0xdf, 0xa3, 0, 0, 0, 0, 0, 0, 0, 0,
    ]);
    expect(await validateVideoFile(file)).toBeNull();
  });

  it("accepts AVI (RIFF + AVI)", async () => {
    const file = await makeFile([
      0x52, 0x49, 0x46, 0x46, 0, 0, 0, 0, 0x41, 0x56, 0x49, 0x20,
    ]);
    expect(await validateVideoFile(file)).toBeNull();
  });

  it("accepts FLV", async () => {
    const file = await makeFile([0x46, 0x4c, 0x56, 0, 0, 0, 0, 0, 0, 0, 0, 0]);
    expect(await validateVideoFile(file)).toBeNull();
  });

  it("accepts MPEG-TS (sync byte 0x47)", async () => {
    const file = await makeFile([0x47, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0]);
    expect(await validateVideoFile(file)).toBeNull();
  });

  it("rejects non-video files", async () => {
    const file = await makeFile([
      0x89, 0x50, 0x4e, 0x47, 0, 0, 0, 0, 0, 0, 0, 0,
    ]);
    const result = await validateVideoFile(file);
    expect(result).not.toBeNull();
    expect(result).toContain("supported video format");
  });

  it("rejects empty data", async () => {
    const file = await makeFile([0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0]);
    const result = await validateVideoFile(file);
    expect(result).not.toBeNull();
  });
});

describe("processed uploads", () => {
  let server: Server;
  let root: ReturnType<typeof createRoot>;
  let container: HTMLDivElement;
  let upload: ReturnType<typeof useUpload>;
  let uploadedBytes: number;
  let published: Record<string, unknown> | undefined;
  let processingResponse: Promise<Response> | undefined;

  function Probe() {
    upload = useUpload();
    return createElement(
      "button",
      { disabled: !upload.canPublish },
      upload.phase.kind,
    );
  }

  beforeEach(async () => {
    vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
    uploadedBytes = 0;
    published = undefined;
    processingResponse = undefined;
    server = createServer(async (request, response) => {
      response.setHeader("Access-Control-Allow-Origin", "*");
      response.setHeader(
        "Access-Control-Allow-Methods",
        "HEAD, PATCH, OPTIONS",
      );
      response.setHeader(
        "Access-Control-Allow-Headers",
        "authorization,tus-resumable,upload-offset,content-type",
      );
      response.setHeader(
        "Access-Control-Expose-Headers",
        "Tus-Resumable,Upload-Offset,Upload-Length",
      );
      if (request.method === "OPTIONS") {
        response.writeHead(204).end();
        return;
      }
      const url = new URL(request.url!, `http://${request.headers.host}`);
      if (url.pathname === "/upload") {
        response.setHeader("Tus-Resumable", "1.0.0");
        if (request.method === "PATCH") {
          for await (const chunk of request) uploadedBytes += chunk.length;
        }
        response.setHeader("Upload-Offset", uploadedBytes);
        response.setHeader("Upload-Length", 12);
        response.writeHead(request.method === "PATCH" ? 204 : 200).end();
        return;
      }
      response.setHeader("Content-Type", "application/json");
      if (url.pathname === "/xrpc/place.stream.media.createUpload") {
        response.end(
          JSON.stringify({
            uploadId: "upload-1",
            uploadUrl: `${url.origin}/upload`,
            uploadToken: "test-upload-token",
            expiresAt: "2099-01-01T00:00:00.000Z",
          }),
        );
      } else if (url.pathname === "/xrpc/place.stream.media.getUploadStatus") {
        response.end(JSON.stringify({ status: "done", durationMs: 438720 }));
      } else if (url.pathname === "/xrpc/place.stream.media.publishVideo") {
        let body = "";
        for await (const chunk of request) body += chunk;
        published = JSON.parse(body);
        response.end(
          JSON.stringify({
            uri: "at://did:plc:abcdefghijklmnopqrstuvwx/place.stream.video/3lvideo",
            cid: "bafkr4ibtxynqf34d34a4q3urq2qrabnshhhfnrv7a7umjakffygwz2aqba",
          }),
        );
      } else {
        response.writeHead(404).end();
      }
    });
    await new Promise<void>((resolve) =>
      server.listen(0, "127.0.0.1", resolve),
    );
    const address = server.address();
    if (!address || typeof address === "string")
      throw new Error("HTTP test server did not bind");
    const origin = `http://127.0.0.1:${address.port}`;
    agent = new StreamplaceAgent({
      did: "did:plc:abcdefghijklmnopqrstuvwx",
      fetchHandler: (path, init) => {
        const response = fetch(new URL(path, origin), init);
        if (String(path).includes("getUploadStatus")) {
          processingResponse = response.then(async (result) => {
            await result.clone().arrayBuffer();
            return result;
          });
          return processingResponse;
        }
        return response;
      },
    });
    container = document.createElement("div");
    root = createRoot(container);
    await act(async () => root.render(createElement(Probe)));
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    await new Promise<void>((resolve) => server.close(() => resolve()));
    vi.unstubAllGlobals();
  });

  it("allows publication when processing is done before track records are published", async () => {
    const file = new File(
      [new Uint8Array([0, 0, 0, 12, 0x66, 0x74, 0x79, 0x70, 0, 0, 0, 0])],
      "test.mp4",
      { type: "video/mp4" },
    );
    await act(async () => {
      upload.selectFile(file);
      upload.setTitle("Uploaded video");
      upload.setDescription("Kept description");
      upload.setTagsDirectly(["demo"]);
    });
    await act(async () => {
      await upload.startUpload();
      await processingResponse;
    });
    expect(upload.phase.kind, JSON.stringify(upload.phase)).not.toBe("error");
    expect(uploadedBytes).toBe(file.size);
    expect(
      container.querySelector("button")!.disabled,
      JSON.stringify(upload.phase),
    ).toBe(false);
    expect(upload.phase).toMatchObject({ kind: "ready", uploadId: "upload-1" });
    await act(async () => upload.publish());
    expect(container.textContent).toBe("done");
    expect(published).toMatchObject({
      uploadId: "upload-1",
      record: {
        title: "Uploaded video",
        description: "Kept description",
        tags: ["demo"],
      },
    });
    expect(published!.record).toHaveProperty("source", {
      $type: "place.stream.media.defs#sourceTracks",
      tracks: [],
    });
    expect(() =>
      place.stream.media.publishVideo.$input.schema!.check(published),
    ).not.toThrow();
  });
});
