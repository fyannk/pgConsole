import React from 'react';
import Layout from '@theme/Layout';
import Link from '@docusaurus/Link';
import useBaseUrl from '@docusaurus/useBaseUrl';

const capabilities = [
  {
    title: 'Every claim has a source',
    body: 'Operator-reported, Kubernetes-observed, and application-derived state stay visibly distinct, so nobody has to guess which component is making a claim.',
  },
  {
    title: 'Failure stays visible',
    body: 'Broken watches, forbidden reads, stale snapshots, and missing fields become unknown or stale. The console never renders uncertainty as health.',
  },
  {
    title: 'Bounded by construction',
    body: 'Event windows, retained objects, messages, and on-demand log tails all carry explicit limits, so one screen cannot pull an unbounded amount of cluster data.',
  },
  {
    title: 'Guarded operations',
    body: 'Backup, reload, restart, and promote are the entire mutation surface — behind proxy-asserted levels, confirmation, CSRF, audit, feature flags, and namespaced RBAC.',
  },
];

export default function Home(): React.ReactElement {
  return (
    <Layout
      title="pgConsole"
      description="A per-cluster operational console for one CloudNativePG cluster: source-attributed, bounded, read-only by default.">
      <header className="pgc-hero">
        <div className="pgc-hero__inner">
          <img
            className="pgc-hero__mark"
            src={useBaseUrl('img/logo.png')}
            alt=""
            width={220}
            height={171}
          />
          <div className="pgc-hero__copy">
            <h1 className="pgc-hero__title">
              pg<span>Console</span>
            </h1>
            <p className="pgc-hero__tagline">
              Operate one CloudNativePG cluster without handing out{' '}
              <code>kubectl</code>.
            </p>
            <div className="pgc-hero__actions">
              <Link className="button button--primary button--lg" to="/docs/">
                Read the documentation
              </Link>
              <Link
                className="button button--outline button--lg pgc-hero__ghost"
                to="https://github.com/fyannk/pgConsole">
                View on GitHub
              </Link>
            </div>
          </div>
        </div>
      </header>

      <main className="pgc-main">
        <p className="pgc-lede">
          pgConsole renders what CloudNativePG and Kubernetes report — status,
          conditions, pods, events, backups, declared database objects, and a
          bounded log tail — attributed to its origin and honest about
          staleness. It authenticates nobody: a trusted proxy asserts the
          user's level, and the console shows only the routes that level
          admits. Its entire authority is the RBAC on its ServiceAccount.
        </p>

        <div className="pgc-grid">
          {capabilities.map((c) => (
            <section className="pgc-card" key={c.title}>
              <h2>{c.title}</h2>
              <p>{c.body}</p>
            </section>
          ))}
        </div>

        <aside className="pgc-note">
          <strong>What it does not claim.</strong> pgConsole reports what the
          operator and Kubernetes say. It does not independently prove
          replication health, data integrity, or restoreability — and it
          provides no SQL access, no database contents, and no Secret reads.
        </aside>
      </main>
    </Layout>
  );
}
