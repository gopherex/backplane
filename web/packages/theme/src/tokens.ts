// Palette adapted from HyperDX; see ../NOTICE.md for source and license.
export const palette = {
  green: ['#eafff6', '#cdfee7', '#a0fad5', '#63f2bf', '#25e2a5', '#00c28a', '#00a475', '#008362', '#00674e', '#005542'],
  gray: ['#fafafa', '#e6e6ee', '#d7d8db', '#aeaeb7', '#a1a1aa', '#868691', '#7e7e8b', '#6c6c79', '#5f5f6e', '#515264'],
  dark: ['#c1c2c5', '#a6a7ab', '#909296', '#5c5f66', '#373a40', '#2c2e33', '#25262b', '#1a1b1e', '#141517', '#101113'],
} as const;

export type ThemeMode = 'dark' | 'light';
export const typography = {
  fontFamily: '"IBM Plex Sans", sans-serif',
  fontFamilyMonospace: '"IBM Plex Mono", monospace',
  fontSize: 14,
  htmlFontSize: 16,
} as const;

export const tokens = {
  dark: {
    background: palette.dark[9], foreground: palette.dark[0],
    card: palette.dark[7], 'card-foreground': palette.dark[0],
    popover: palette.dark[7], 'popover-foreground': palette.dark[0],
    primary: palette.green[3], 'primary-foreground': palette.dark[9],
    secondary: palette.dark[6], 'secondary-foreground': palette.dark[0],
    muted: palette.dark[7], 'muted-foreground': palette.dark[2],
    accent: palette.dark[5], 'accent-foreground': palette.dark[0],
    destructive: '#ff8787', 'destructive-foreground': '#3d0d0d',
    success: palette.green[4], 'success-foreground': '#0d2913',
    warning: '#ffe066', 'warning-foreground': '#3c3310',
    info: '#74c0fc', 'info-foreground': '#071e34',
    border: palette.dark[4], input: palette.dark[3], ring: palette.green[4],
    link: palette.green[4], radius: '4px',
  },
  light: {
    background: '#ffffff', foreground: palette.dark[9],
    card: '#ffffff', 'card-foreground': palette.dark[9],
    popover: '#ffffff', 'popover-foreground': palette.dark[9],
    primary: palette.green[7], 'primary-foreground': '#ffffff',
    secondary: '#f6f6fa', 'secondary-foreground': palette.dark[9],
    muted: '#f6f6fa', 'muted-foreground': palette.gray[7],
    accent: '#ebebef', 'accent-foreground': palette.dark[9],
    destructive: '#c92a2a', 'destructive-foreground': '#ffffff',
    success: '#004838', 'success-foreground': '#eafff6',
    warning: '#775500', 'warning-foreground': '#fff9db',
    info: '#1864ab', 'info-foreground': '#e7f5ff',
    border: palette.gray[2], input: palette.gray[6], ring: palette.green[8],
    link: palette.green[8], radius: '4px',
  },
} as const;

export function cssVariables(mode: ThemeMode): Record<string, string> {
  return {
    ...Object.fromEntries(Object.entries(tokens[mode]).map(([key, value]) => [`--${key}`, value])),
    '--font-sans': typography.fontFamily,
    '--font-mono': typography.fontFamilyMonospace,
  };
}
