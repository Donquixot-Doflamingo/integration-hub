/** 20x20 stroke icons (1.6px), drawn for this console. Paths only; colour comes from currentColor. */
export const icons = {
	// two endpoints joined through the hub
	integrations: 'M3 6h4m6 0h4M3 14h4m6 0h4M7 6c3 0 3 8 6 8M7 14c3 0 3-8 6-8',
	// pulse line
	health: 'M2 10h4l2-5 4 10 2-5h4',
	// stacked request rows
	requests: 'M4 5h12M4 10h12M4 15h8',
	// tray with a stopped item
	dlq: 'M3 12l2-7h10l2 7v4H3zM3 12h4l1 2h4l1-2h4M8 8h4',
	// bracket -> bracket transform
	transformers: 'M7 4L3 10l4 6M13 4l4 6-4 6M11 6l-2 8',
	// plug: a vendor API the Hub connects to
	vendors: 'M7 3v4M13 3v4M5 7h10v3a5 5 0 0 1-10 0zM10 15v3',
	// key: a credentialed account
	accounts: 'M7.5 12.5a3.5 3.5 0 1 0 0-7 3.5 3.5 0 0 0 0 7zM10.5 9H17M14 9v3M16.5 9v2',
	// two people
	tenants:
		'M7 9a2.5 2.5 0 1 0 0-5 2.5 2.5 0 0 0 0 5zM2.5 16c.5-3 2.3-4.5 4.5-4.5s4 1.5 4.5 4.5M13.5 8.5a2 2 0 1 0 0-4M14 11.5c1.8.3 3 1.7 3.5 4.5',
	// layered sheets
	snapshots: 'M10 3l7 3.5-7 3.5-7-3.5zM3 10l7 3.5 7-3.5M3 13.5L10 17l7-3.5',
	// clock with list
	changelog: 'M10 17a7 7 0 1 0 0-14 7 7 0 0 0 0 14zM10 6v4l3 2',
	menu: 'M3 5h14M3 10h14M3 15h14',
	close: 'M5 5l10 10M15 5L5 15',
	sun: 'M10 13.5a3.5 3.5 0 1 0 0-7 3.5 3.5 0 0 0 0 7zM10 1.5v2M10 16.5v2M1.5 10h2M16.5 10h2M4 4l1.4 1.4M14.6 14.6L16 16M4 16l1.4-1.4M14.6 5.4L16 4',
	moon: 'M16 12.5A6.5 6.5 0 0 1 7.5 4a6.5 6.5 0 1 0 8.5 8.5z',
	system: 'M3 4h14v9H3zM7 17h6M10 13v4',
	copy: 'M7 7h9v10H7zM4 13V3h9',
	check: 'M4 10.5l4 4 8-9',
	retry: 'M15.5 7A6 6 0 1 0 16 12M16 3v4h-4',
	alert: 'M10 3l8 14H2zM10 8v4M10 14.5v.5',
	chevron: 'M7 4l6 6-6 6',
	open: 'M10 3a7 7 0 1 0 0 14 7 7 0 0 0 0-14zM7 7l6 6M13 7l-6 6',
	halfOpen: 'M10 3a7 7 0 1 0 0 14 7 7 0 0 0 0-14zM10 3v14',
	closed: 'M10 3a7 7 0 1 0 0 14 7 7 0 0 0 0-14zM6.5 10l2.5 2.5 4.5-5',
	pause: 'M10 3a7 7 0 1 0 0 14 7 7 0 0 0 0-14zM8 7v6M12 7v6',
	// padlock: sealed payloads
	lock: 'M5 9h10v8H5zM7 9V6.5a3 3 0 0 1 6 0V9M10 12.5v2'
} as const;

export type IconName = keyof typeof icons;
