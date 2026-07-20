import { useQuery } from "@tanstack/react-query";
import { Link, createFileRoute } from "@tanstack/react-router";
import { PolicyGate } from "../policy";
import { instanceQuery, pageQuery } from "../queries";
import { Card, Markdown, Spinner } from "../ui";

// A content page lives outside the authenticated shell: a rules or sponsors page is public, so an
// anonymous visitor must be able to open it by URL. The per-page draft/auth_required gate is the
// server's — a draft reads as 404, an auth-gated page as an auth-required denial the PolicyGate
// turns into a sign-in prompt.
export const Route = createFileRoute("/pages/$route")({
  component: PageView,
});

function PageView() {
  const { route } = Route.useParams();
  const instance = useQuery(instanceQuery);
  const page = useQuery(pageQuery(route));

  return (
    <div className="ff-stack" style={{ margin: "var(--space-7) auto 0", maxWidth: "44rem" }}>
      <Link to="/challenges" className="brand">
        {instance.data?.ctf_name ?? "flagfish"}
        <span className="cursor">_</span>
      </Link>

      {page.isPending ? (
        <Spinner />
      ) : page.isError ? (
        <PolicyGate error={page.error} />
      ) : (
        <Card>
          <article className="ff-stack">
            <h1>{page.data.title}</h1>
            <Markdown source={page.data.content} />
          </article>
        </Card>
      )}
    </div>
  );
}
