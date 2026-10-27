// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';

export default defineConfig({
	site: 'https://rivolabs.github.io',
	base: '/fxship',
	integrations: [
		starlight({
			title: 'fxship',
			description: 'Ship to your FiveM server from GitHub, tested on dev and approved in Discord.',
			social: [{ icon: 'github', label: 'GitHub', href: 'https://github.com/RivoLabs/fxship' }],
			editLink: { baseUrl: 'https://github.com/RivoLabs/fxship/edit/main/docs/' },
			lastUpdated: true,
			sidebar: [
				{
					label: 'Start here',
					items: [
						{ label: 'What fxship does', slug: 'index' },
						{ label: 'Before you start', slug: 'start/requirements' },
						{ label: 'Put your server in git', slug: 'guides/server-in-git' },
						{ label: 'Make the Discord bot', slug: 'guides/discord-bot' },
						{ label: 'Install on Ubuntu', slug: 'install/linux' },
						{ label: 'Install on Windows Server', slug: 'install/windows' },
					],
				},
				{
					label: 'Every day',
					items: [
						{ label: 'Deploying changes', slug: 'guides/deploying' },
						{ label: 'Editing on dev', slug: 'guides/dev-edits' },
						{ label: 'Staff access', slug: 'guides/staff' },
						{ label: 'The database', slug: 'guides/database' },
						{ label: 'Backups and restores', slug: 'guides/backups' },
						{ label: 'Monitoring', slug: 'guides/monitoring' },
					],
				},
				{
					label: 'Reference',
					items: [
						{ label: 'Settings', slug: 'reference/configuration' },
						{ label: 'Commands', slug: 'reference/cli' },
						{ label: 'Discord', slug: 'reference/discord' },
						{ label: 'How it works', slug: 'reference/how-it-works' },
						{ label: 'Troubleshooting', slug: 'reference/troubleshooting' },
						{ label: 'Upgrading and removing', slug: 'reference/upgrading' },
					],
				},
			],
		}),
	],
});
