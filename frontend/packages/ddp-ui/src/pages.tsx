import { Component, type ComponentType, type ReactNode } from "react";

const pages = new Map<string, ComponentType>();

export function registerPage(id: string, Page: ComponentType): void {
  if (!id.trim()) throw new Error("Page id must not be empty.");
  if (pages.has(id)) throw new Error(`Page already registered: ${id}`);
  pages.set(id, Page);
}

export function getPage(id: string): ComponentType | undefined {
  return pages.get(id);
}

type PageErrorBoundaryProps = { children: ReactNode };
type PageErrorBoundaryState = { failed: boolean };

export class PageErrorBoundary extends Component<PageErrorBoundaryProps, PageErrorBoundaryState> {
  state: PageErrorBoundaryState = { failed: false };

  static getDerivedStateFromError(): PageErrorBoundaryState {
    return { failed: true };
  }

  render(): ReactNode {
    if (!this.state.failed) return this.props.children;
    return <section className="page-error" role="alert"><strong>Page failed to load.</strong><span>Try again or open another workspace page.</span><button type="button" onClick={() => this.setState({ failed: false })}>Try again</button></section>;
  }
}
