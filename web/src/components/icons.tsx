// Line icons, 24px grid, stroke = currentColor.
import type { ReactNode, SVGProps } from 'react';

function I({ children, ...p }: SVGProps<SVGSVGElement> & { children: ReactNode }) {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={1.8} strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" {...p}>
      {children}
    </svg>
  );
}

export const Logo = (p: SVGProps<SVGSVGElement>) => (
  <svg viewBox="0 0 48 48" fill="none" stroke="currentColor" strokeWidth={3} strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" {...p}>
    <path d="M6 22 24 7l18 15" />
    <path d="M32 13V6h5v11" />
    <path d="M10 19v21h28V19" />
    <path d="M13 30h6l3-6 4 11 3-5h6" />
  </svg>
);

export const IconDevices = () => (
  <I><rect x="3" y="4" width="18" height="6" rx="1.5" /><rect x="3" y="14" width="18" height="6" rx="1.5" /><path d="M7 7h.01M7 17h.01" /></I>
);
export const IconSettings = () => (
  <I><circle cx="12" cy="12" r="3" /><path d="M19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.8-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1.1-1.5 1.7 1.7 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.8 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1a1.7 1.7 0 0 0 1.5-1.1 1.7 1.7 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.8.3H9a1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.8V9a1.7 1.7 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z" /></I>
);
export const IconLogout = () => (<I><path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4" /><path d="m16 17 5-5-5-5M21 12H9" /></I>);
export const IconMenu = () => (<I><path d="M4 6h16M4 12h16M4 18h16" /></I>);
export const IconPlus = () => (<I><path d="M12 5v14M5 12h14" /></I>);
export const IconCards = () => (<I><rect x="3" y="3" width="7" height="7" rx="1" /><rect x="14" y="3" width="7" height="7" rx="1" /><rect x="3" y="14" width="7" height="7" rx="1" /><rect x="14" y="14" width="7" height="7" rx="1" /></I>);
export const IconTable = () => (<I><path d="M3 6h18M3 12h18M3 18h18" /></I>);
export const IconTree = () => (<I><path d="M4 5h9M9 12h11M9 19h11M6 5v14h3M6 12h3" /></I>);
export const IconRefresh = () => (<I><path d="M21 12a9 9 0 1 1-2.6-6.4" /><path d="M21 4v5h-5" /></I>);
export const IconTrash = () => (<I><path d="M3 6h18M8 6V4h8v2M6 6l1 14h10l1-14" /></I>);
export const IconEdit = () => (<I><path d="M12 20h9" /><path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4z" /></I>);
export const IconKey = () => (<I><circle cx="7.5" cy="15.5" r="4.5" /><path d="m10.7 12.3 9.8-9.8M17 6l3 3M14 9l2 2" /></I>);
export const IconInbox = () => (<I><path d="M22 12h-6l-2 3h-4l-2-3H2" /><path d="M5.5 5h13L22 12v6a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2v-6z" /></I>);
export const IconPuzzle = () => (<I><path d="M10 3h4v3a2 2 0 1 0 4 0V3h3v7h-3a2 2 0 1 0 0 4h3v7h-7v-3a2 2 0 1 0-4 0v3H3v-7h3a2 2 0 1 0 0-4H3V3z" /></I>);
export const IconJobs = () => (<I><path d="M8 6h13M8 12h13M8 18h13" /><path d="m3 6 1 1 2-2M3 12l1 1 2-2M3 18l1 1 2-2" /></I>);
export const IconUpdates = () => (<I><path d="M12 3v12" /><path d="m7 8 5-5 5 5" /><path d="M5 21h14" /></I>);
export const IconBackup = () => (<I><ellipse cx="12" cy="5" rx="8" ry="3" /><path d="M4 5v6c0 1.7 3.6 3 8 3s8-1.3 8-3V5" /><path d="M4 11v6c0 1.7 3.6 3 8 3s8-1.3 8-3v-6" /></I>);
export const IconX = () => (<I><path d="M18 6 6 18M6 6l12 12" /></I>);
export const IconEye = () => (<I><path d="M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7S2 12 2 12z" /><circle cx="12" cy="12" r="3" /></I>);
export const IconChevron = ({ open }: { open?: boolean }) => (<I style={{ transform: open ? 'rotate(90deg)' : undefined, transition: 'transform .15s' }}><path d="m9 6 6 6-6 6" /></I>);
export const IconOverview = () => (<I><rect x="3" y="3" width="7" height="9" rx="1.5" /><rect x="14" y="3" width="7" height="5" rx="1.5" /><rect x="14" y="12" width="7" height="9" rx="1.5" /><rect x="3" y="16" width="7" height="5" rx="1.5" /></I>);
export const IconPulse = () => (<I><path d="M3 12h4l3-7 4 14 3-7h4" /></I>);
