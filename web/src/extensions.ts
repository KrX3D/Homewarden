// Extension points that later features (updates, integrations, backups) fill in,
// so the device page does not need to know about each of them.
import type { ComponentType } from 'react';
import type { Device } from './api';

/** Extra sections rendered on the device detail page, in order. */
export const deviceSections: ComponentType<{ device: Device }>[] = [];
