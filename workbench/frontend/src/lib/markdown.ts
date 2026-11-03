/** The agent's Markdown as safe HTML. The agent repeats what it reads, so its output is untrusted. */
import DOMPurify from "dompurify";
import { Marked } from "marked";

const marked = new Marked({ gfm: true, breaks: false, async: false });

let hooked = false;
function hookLinks() {
  if (hooked) return;
  hooked = true;
  // Links open outside the workbench and cannot reach back into it.
  DOMPurify.addHook("afterSanitizeAttributes", (node) => {
    if (node.tagName === "A") {
      node.setAttribute("target", "_blank");
      node.setAttribute("rel", "noopener noreferrer");
    }
  });
}

export function renderMarkdown(text: string): string {
  hookLinks();
  // A table scrolls sideways in its own box instead of crushing its columns.
  const html = (marked.parse(text) as string).trim()
    .replaceAll("<table>", '<div class="table-scroll"><table>').replaceAll("</table>", "</table></div>");
  return DOMPurify.sanitize(html, { FORBID_TAGS: ["style", "form", "input", "img"], FORBID_ATTR: ["style"] });
}

/** One line of Markdown (code spans, emphasis, links) as safe inline HTML, with no paragraph around it. */
export function renderInline(text: string): string {
  hookLinks();
  const html = marked.parseInline(text) as string;
  return DOMPurify.sanitize(html, { ALLOWED_TAGS: ["code", "em", "strong", "a", "del"], ALLOWED_ATTR: ["href", "target", "rel"] });
}

/**
 * An agent's question as safe HTML, one paragraph per line. Agents write a
 * question as lines ("Done: ...", "Needs a person: ...", "Question: ..."),
 * which Markdown would run together into one block. List items, tables and
 * code fences keep their Markdown meaning.
 */
export function renderQuestion(text: string): string {
  const out: string[] = [];
  let fenced = false;
  for (const line of text.replace(/\r\n/g, "\n").split("\n")) {
    const t = line.trim();
    if (t.startsWith("```")) fenced = !fenced;
    const structural = fenced || /^([-*+] |\d+[.)] |\||#|>)/.test(t) || /^\s/.test(line);
    const prev = out.length ? out[out.length - 1] : "";
    if (!structural && t && prev.trim()) out.push("");
    out.push(line);
  }
  return renderMarkdown(out.join("\n"));
}
