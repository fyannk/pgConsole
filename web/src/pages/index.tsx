import React from 'react';
import Layout from '@theme/Layout';
import Link from '@docusaurus/Link';

export default function Home(): React.ReactElement {
  return (
    <Layout
      title="pgConsole"
      description="An operational console for one CloudNativePG cluster, read-only by default">
      <main style={{maxWidth: 'var(--ifm-container-width)', margin: '0 auto', padding: '4rem 1rem'}}>
        <h1>pgConsole</h1>
        <p>
          A per-cluster operational console for one CloudNativePG cluster,
          read-only by default. It renders what the operator and Kubernetes
          report — status, pods, events, backups, and a bounded log tail —
          attributed to its origin and honest about staleness, without giving
          anyone <code>kubectl</code> access or a database connection.
        </p>
        <p>
          The console authenticates nobody: a trusted proxy asserts the
          user's level, and the console shows the routes that level admits.
          Its entire authority is the RBAC on its ServiceAccount.
        </p>
        <p>
          <Link className="button button--primary button--lg" to="/docs/">
            Read the documentation
          </Link>
        </p>
      </main>
    </Layout>
  );
}
