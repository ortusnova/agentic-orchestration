/** The app's places, kept in the URL hash so links and reloads land where they were. */
import type { ViewState } from "./types";

export type ProjectPage = "home" | "graph" | "workers";

export type Route =
  | { name: "projects" }
  | { name: "new" }
  | { name: "project"; slug: string; page: ProjectPage; view: ViewState };

export const emptyView = (): ViewState => ({ epic: null, selected: null, showCross: true, mark: null });

export function parseRoute(hash: string): Route {
  const raw = hash.replace(/^#/, "");
  const [path, query = ""] = raw.split("?");
  const parts = path.split("/").filter(Boolean);
  if (parts[0] === "new" && parts.length === 1) return { name: "new" };
  if (parts[0] === "p" && parts[1] && parts.length === 3 && parts[2] === "workers") {
    return { name: "project", slug: decodeURIComponent(parts[1]), page: "workers", view: emptyView() };
  }
  if (parts[0] === "p" && parts[1] && (parts.length === 2 || (parts.length === 3 && parts[2] === "graph"))) {
    const q = new URLSearchParams(query);
    const view: ViewState = { epic: q.get("epic"), selected: q.get("sel"), showCross: q.get("cross") !== "0", mark: q.get("mark"), watch: q.get("watch") };
    // A link from before the home existed carries the graph's place without "/graph".
    const page: ProjectPage = parts[2] === "graph" || view.epic || view.selected || view.mark || !view.showCross ? "graph" : "home";
    return { name: "project", slug: decodeURIComponent(parts[1]), page, view };
  }
  return { name: "projects" };
}

export function formatRoute(route: Route): string {
  switch (route.name) {
    case "projects":
      return "#/";
    case "new":
      return "#/new";
    case "project": {
      const base = `#/p/${encodeURIComponent(route.slug)}`;
      if (route.page === "home") return base;
      if (route.page === "workers") return `${base}/workers`;
      const q = new URLSearchParams();
      if (route.view.epic) q.set("epic", route.view.epic);
      if (route.view.selected) q.set("sel", route.view.selected);
      if (route.view.mark) q.set("mark", route.view.mark);
      if (!route.view.showCross) q.set("cross", "0");
      if (route.view.watch) q.set("watch", route.view.watch);
      const s = q.toString();
      return `${base}/graph${s ? `?${s}` : ""}`;
    }
  }
}

/** The graph at a place: a story selected in its epic, an epic opened, or a milestone's changes marked. */
export function graphHref(slug: string, place: { epic?: string | null; selected?: string | null; mark?: string | null; watch?: string | null } = {}): string {
  return formatRoute({ name: "project", slug, page: "graph", view: { ...emptyView(), ...place } });
}

/** A story in the graph with its worker's session open in the chat pane: live while it runs, its last transcript after. */
export const watchHref = (slug: string, key: string, identifier: string) =>
  graphHref(slug, { epic: key.split("/")[0], selected: key, watch: identifier });

export const workersHref = (slug: string) => formatRoute({ name: "project", slug, page: "workers", view: emptyView() });

export const homeHref = (slug: string) => formatRoute({ name: "project", slug, page: "home", view: emptyView() });

export const projectHref = homeHref;

/** A slug suggested from a project name, as the backend derives one. */
export function suggestSlug(name: string): string {
  return name.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 48).replace(/-+$/, "");
}

/** A Huly project key suggested from a name: the initials of its words, or the start of a single word. */
export function suggestKey(name: string): string {
  const words = name.toUpperCase().replace(/[^A-Z0-9]+/g, " ").trim().split(" ").filter(Boolean);
  const key = words.length > 1 ? words.map((w) => w[0]).join("") : (words[0] ?? "").slice(0, 4);
  return key.replace(/^[0-9]+/, "").slice(0, 5);
}
