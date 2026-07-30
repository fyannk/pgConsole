import {themes as prismThemes} from 'prism-react-renderer';
import type {Config} from '@docusaurus/types';
import type * as Preset from '@docusaurus/preset-classic';

// This runs in Node.js - Don't use client-side code here (browser APIs, JSX...)

const config: Config = {
  title: 'pgConsole',
  tagline: 'A read-only operational console for one CloudNativePG cluster',
  favicon: 'img/favicon.svg',

  // GitHub Pages for fyannk/pgconsole.
  url: 'https://fyannk.github.io',
  baseUrl: '/pgconsole/',
  trailingSlash: true,

  organizationName: 'fyannk',
  projectName: 'pgconsole',

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
            current: {
              label: 'Dev',
              badge: true,
              banner: 'unreleased',
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
    navbar: {
      title: 'pgConsole',
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
      ],
    },
    footer: {
      style: 'dark',
      links: [
        {
          title: 'pgtoolbox family',
          items: [
            {
              label: 'pgToolBox operator',
              href: 'https://fyannk.github.io/pgtoolbox/',
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
