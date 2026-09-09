import {themes} from 'prism-react-renderer';

export default {
  title: 'agent-go',
  tagline: 'An iMessage agent that runs on your Mac.',
  favicon: 'img/mark.svg',
  url: 'https://teslashibe.github.io',
  baseUrl: '/agent-go/',
  trailingSlash: true,
  organizationName: 'teslashibe',
  projectName: 'agent-go',
  onBrokenLinks: 'throw',
  onBrokenAnchors: 'throw',
  markdown: {
    format: 'md',
    hooks: {onBrokenMarkdownLinks: 'throw', onBrokenMarkdownImages: 'throw'},
  },
  i18n: {defaultLocale: 'en', locales: ['en']},
  presets: [
    ['classic', {
      docs: {
        path: '..',
        include: ['docs/**/*.md', 'CONTRIBUTING.md', 'SECURITY.md', 'SUPPORT.md'],
        exclude: ['docs/open-source-release.md'],
        routeBasePath: '/',
        sidebarPath: './sidebars.js',
      },
      blog: false,
      pages: false,
      theme: {customCss: './src/css/custom.css'},
    }],
  ],
  themeConfig: {
    navbar: {
      title: 'agent-go',
      logo: {alt: '', src: 'img/mark.svg'},
      items: [
        {type: 'doc', docId: 'docs/index', label: 'Docs', position: 'left'},
        {type: 'doc', docId: 'docs/verified', label: 'What works', position: 'left'},
        {href: 'https://github.com/teslashibe/agent-go', label: 'GitHub', position: 'right'},
      ],
    },
    colorMode: {defaultMode: 'light', respectPrefersColorScheme: true},
    docs: {sidebar: {hideable: true}},
    footer: {
      style: 'light',
      copyright: '© 2026 agent-go contributors · MIT · Built with Docusaurus',
    },
    prism: {theme: themes.github, darkTheme: themes.dracula, additionalLanguages: ['bash', 'go', 'json', 'python']},
  },
};
