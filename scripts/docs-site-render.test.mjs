import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { spawnSync } from "node:child_process";
import test from "node:test";

import {
  renderMarkdown,
  tocFromHeadings,
} from "./docs-site-render.mjs";

const keepHref = (href) => href;

test("renderMarkdown escapes raw HTML inside link labels", () => {
  const { html } = renderMarkdown("[<br>](https://example.com)\n[<img src=x onerror=alert(1)>](https://example.com)", "index.md", keepHref);

  assert.match(html, /<a href="https:\/\/example\.com">&lt;br&gt;<\/a>/);
  assert.doesNotMatch(html, /<a href="https:\/\/example\.com"><br><\/a>/);
  assert.doesNotMatch(html, /<img\b/);
});

test("renderMarkdown gives duplicate headings stable unique ids", () => {
  const { html } = renderMarkdown("## Examples\n\n## Examples\n\n### Examples", "index.md", keepHref);

  assert.match(html, /<h2 id="examples">/);
  assert.match(html, /<h2 id="examples-2">/);
  assert.match(html, /<h3 id="examples-3">/);
});

test("renderMarkdown preserves links nested inside heading emphasis", () => {
  const { html, headings } = renderMarkdown(
    "## **[the docs](https://example.com)** and *<https://example.com>*",
    "index.md",
    keepHref,
  );

  assert.match(html, /<strong><a href="https:\/\/example\.com">the docs<\/a><\/strong>/);
  assert.match(html, /<em><a href="https:\/\/example\.com">https:\/\/example\.com<\/a><\/em>/);
  assert.equal(headings[0].label, "the docs and https://example.com");
});

test("renderMarkdown builds safe TOC labels from parser-owned heading facts", () => {
  const markdown = [
    "## AT&T &amp; with **strong**, *emphasis*, and `code`",
    "",
    "> ### Read [**the docs**](https://example.com) or <https://example.com>",
    ">",
    "> ## AT&T &amp; with **strong**, *emphasis*, and `code`",
    "",
    '### [<img src=x onerror=alert(1)>](https://example.com) <scr<script>ipt>alert("toc")</scr<script>ipt>',
  ].join("\n");
  const { html, headings } = renderMarkdown(markdown, "index.md", keepHref);
  const toc = tocFromHeadings(headings);

  assert.deepEqual(headings, [
    {
      level: 2,
      id: "at-t-amp-with-strong-emphasis-and-code",
      label: "AT&T &amp; with strong, emphasis, and code",
    },
    {
      level: 3,
      id: "read-the-docs-https-example-com-or-https-example-com",
      label: "Read the docs or https://example.com",
    },
    {
      level: 2,
      id: "at-t-amp-with-strong-emphasis-and-code-2",
      label: "AT&T &amp; with strong, emphasis, and code",
    },
    {
      level: 3,
      id: "img-src-x-onerror-alert-1-https-example-com-scr-script-ipt-alert-toc-scr-script-ipt",
      label: '<img src=x onerror=alert(1)> <scr<script>ipt>alert("toc")</scr<script>ipt>',
    },
  ]);
  assert.match(html, /<blockquote><h3 id="read-the-docs-https-example-com-or-https-example-com">/);
  assert.match(html, /<h2 id="at-t-amp-with-strong-emphasis-and-code-2">/);
  assert.match(toc, />AT&amp;T &amp;amp; with strong, emphasis, and code<\/a>/);
  assert.match(toc, />&lt;img src=x onerror=alert\(1\)&gt; &lt;scr&lt;script&gt;ipt&gt;alert\(&quot;toc&quot;\)&lt;\/scr&lt;script&gt;ipt&gt;<\/a>/);
  assert.doesNotMatch(toc, /AT&amp;amp;T/);
  assert.equal(toc.includes("<img"), false);
  assert.equal(toc.includes("<script>"), false);
});

test("tocFromHeadings omits zero or one heading", () => {
  assert.equal(tocFromHeadings([]), "");
  assert.equal(tocFromHeadings([{ level: 2, id: "only", label: "Only" }]), "");
});

test("docs site build renders structured heading facts into the generated page", (t) => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "wacli-docs-site-"));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  fs.mkdirSync(path.join(root, "docs"));
  fs.writeFileSync(path.join(root, "docs", "index.md"), "# Home\n");
  fs.writeFileSync(
    path.join(root, "docs", "install.md"),
    "# Install\n\n## AT&T **bold**\n\n### [<img onerror=alert(1)>](https://example.com)\n",
  );
  fs.writeFileSync(path.join(root, "docs", "quickstart.md"), "# Quickstart\n");
  const result = spawnSync(process.execPath, [path.resolve("scripts/build-docs-site.mjs")], {
    cwd: root,
    encoding: "utf8",
  });

  assert.equal(result.status, 0, result.stderr);
  const page = fs.readFileSync(path.join(root, "dist", "docs-site", "install.html"), "utf8");
  assert.match(page, /href="#at-t-bold">AT&amp;T bold<\/a>/);
  assert.match(page, /href="#img-onerror-alert-1-https-example-com">&lt;img onerror=alert\(1\)&gt;<\/a>/);
  assert.equal(page.includes("<img onerror="), false);
});
