import {themes as prismThemes} from 'prism-react-renderer';
import type {Config} from '@docusaurus/types';
import type * as Preset from '@docusaurus/preset-classic';

// This runs in Node.js - Don't use client-side code here (browser APIs, JSX...)

const config: Config = {
  title: 'pgConsole',
  tagline: 'An operational console for one CloudNativePG cluster, read-only by default',
  favicon: 'img/favicon.ico',

  // GitHub Pages for fyannk/pgConsole.
  url: 'https://fyannk.github.io',
  baseUrl: '/pgConsole/',
  trailingSlash: true,

  organizationName: 'fyannk',
  projectName: 'pgConsole',

  onBrokenLinks: 'throw',

  markdown: {
    mermaid: true,
    hooks: {
      onBrokenMarkdownLinks: 'throw',
    },
  },

  i18n: {
    defaultLocale: 'en',
    locales: ['en'],
  },

  presets: [
    [
      'classic',
      {
        docs: {
          path: 'docs',
          sidebarPath: './sidebars.ts',
          includeCurrentVersion: true,
          versions: {
            // There is no versioned_docs snapshot yet, so "current" is the
            // released documentation. It must not carry the "unreleased"
            // banner, which would put that warning on every published page.
            current: {
              label: '0.4.0',
              badge: true,
              banner: 'none',
            },
          },
        },
        blog: false,
        theme: {
          customCss: './src/css/custom.css',
        },
      } satisfies Preset.Options,
    ],
  ],
  themes: [
    '@docusaurus/theme-mermaid',
    [
      require.resolve('@easyops-cn/docusaurus-search-local'),
      {
        hashed: true,
        docsDir: ['docs'],
        searchResultLimits: 8,
        searchResultContextMaxLength: 50,
        language: ['en'],
        indexBlog: false,
        indexPages: false,
      },
    ],
  ],
  themeConfig: {
    // The card unfurled by chat clients and social previews: the same
    // lockup the console's own topbar shows, so a link to the docs is
    // recognisably the product.
    image: 'img/social-card.png',
    metadata: [
      {
        name: 'description',
        content:
          'pgConsole is a per-cluster operational console for one CloudNativePG cluster: source-attributed, bounded, read-only by default.',
      },
    ],
    colorMode: {
      respectPrefersColorScheme: true,
    },
    navbar: {
      title: 'pgConsole',
      logo: {
        alt: 'pgConsole',
        src: 'img/logo.png',
        // The navbar is navy in both themes, so the mark is the same file.
        // Without srcDark, Docusaurus renders only a light-themed image and
        // the logo disappears entirely in dark mode.
        srcDark: 'img/logo.png',
      },
      items: [
        {
          type: 'docSidebar',
          sidebarId: 'docs',
          position: 'left',
          label: 'Documentation',
        },
        {
          type: 'docsVersionDropdown',
          position: 'right',
        },
        {
          href: 'https://github.com/fyannk/pgConsole',
          position: 'right',
          className: 'navbar-github',
          'aria-label': 'pgConsole on GitHub',
        },
      ],
    },
    footer: {
      style: 'dark',
      logo: {
        alt: 'pgConsole',
        src: 'img/logo.png',
        srcDark: 'img/logo.png',
        href: 'https://github.com/fyannk/pgConsole',
        width: 84,
      },
      copyright: `Copyright © ${new Date().getFullYear()} pgConsole contributors. Apache-2.0 licensed.`,
      links: [
        {
          title: 'Project',
          items: [
            {
              label: 'GitHub',
              href: 'https://github.com/fyannk/pgConsole',
            },
            {
              label: 'Releases',
              href: 'https://github.com/fyannk/pgConsole/releases',
            },
            {
              label: 'Report a vulnerability',
              href: 'https://github.com/fyannk/pgConsole/security/policy',
            },
          ],
        },
        {
          title: 'CloudNativePG',
          items: [
            {
              label: 'Website',
              href: 'https://cloudnative-pg.io',
            },
            {
              label: 'Slack',
              href: 'https://cloud-native.slack.com/messages/cloudnativepg-users',
            },
          ],
        },
      ],
    },
    prism: {
      theme: prismThemes.github,
      darkTheme: prismThemes.dracula,
      additionalLanguages: ['bash', 'yaml', 'go'],
    },
  } satisfies Preset.ThemeConfig,
};

export default config;
