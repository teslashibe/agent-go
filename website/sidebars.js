export default {
  docs: [
    {type: 'doc', id: 'docs/index', label: 'Introduction'},
    {type: 'doc', id: 'docs/verified', label: 'Verified capabilities'},
    {type: 'category', label: 'Use the agent', collapsed: false, items: [
      {type: 'doc', id: 'docs/install-macos', label: 'Install on macOS'},
      {type: 'doc', id: 'docs/capabilities', label: 'Optional capabilities'},
      {type: 'doc', id: 'docs/google', label: 'Google integration'},
      {type: 'doc', id: 'docs/operations', label: 'Operations & recovery'},
    ]},
    {type: 'category', label: 'Develop & contribute', collapsed: false, items: [
      {type: 'doc', id: 'docs/mini-development', label: 'Mac mini verification'},
      {type: 'doc', id: 'CONTRIBUTING', label: 'Contributing'},
      {type: 'doc', id: 'docs/documentation', label: 'Documentation site'},
      {type: 'doc', id: 'SUPPORT', label: 'Support'},
      {type: 'doc', id: 'SECURITY', label: 'Security'},
    ]},
  ],
};
