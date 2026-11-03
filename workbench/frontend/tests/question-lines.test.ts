import { describe, expect, it } from "vitest";
import { renderQuestion } from "../src/lib/markdown";

// ONS-91's question as Sortie served it (2026-11-02): one thought per line,
// which the Answer card ran together into one unreadable block.
const ONS_91 = [
  "Done in this repo: worker-daemon step/unmanaged reporting, workbench Workers view, home slot lanes and in-place actions, docs (see .sortie/pr.json).",
  "Needs a person: the Sortie PR. This sandbox has no GitHub access and no sortie/ submodule.",
  "Should that be a separate story run in ortusnova/sortie?",
  "The backend route tests (tests/workbench/test_supervision.py) are unrun: there is no Python here.",
].join("\n");

const paragraphs = (html: string) => {
  const el = document.createElement("div");
  el.innerHTML = html;
  return [...el.querySelectorAll(":scope > p")].map((p) => p.textContent?.trim());
};

describe("an agent's question", () => {
  it("shows each line of the question as its own paragraph", () => {
    expect(paragraphs(renderQuestion(ONS_91))).toEqual(ONS_91.split("\n"));
  });

  it("keeps lists, code and blank-line paragraphs as Markdown", () => {
    const el = document.createElement("div");
    el.innerHTML = renderQuestion("Two things:\n- first\n- second\n\n```\na\nb\n```\nLast line");
    expect([...el.querySelectorAll("li")].map((li) => li.textContent)).toEqual(["first", "second"]);
    expect(el.querySelector("pre code")?.textContent).toBe("a\nb\n");
    expect(paragraphs(el.innerHTML)).toEqual(["Two things:", "Last line"]);
  });
});
