<script lang="ts">
    import { onMount } from 'svelte';

    type Runtime = {
        configSet: string;
        urls: Record<string, string>;
        dnsRefresh?: DNSRefreshStatus;
    };

    type DNSRefreshStatus = {
        enabled: boolean;
        running: boolean;
        currentSite?: string;
        processed: number;
        total: number;
        resolved: number;
        added: number;
        lastStartedAt?: string;
        lastFinishedAt?: string;
        nextRunAt?: string;
        lastError?: string;
    };

    type Site = {
        name: string;
        group: string;
        icon: string;
        counts: Record<string, number>;
        dynamicCounts?: Record<string, number>;
        dynamicTotal?: number;
    };

    type Group = {
        name: string;
        sites: Site[];
    };

    type Option = {
        value: string;
        label: string;
    };

    type SelectKey = 'format' | 'data';
    type SelectionMode = 'include' | 'exclude';

    const formats = [
        { value: 'unifi', label: 'UniFi' },
        { value: 'text', label: 'Text' },
        { value: 'json', label: 'JSON' },
        { value: 'mikrotik', label: 'MikroTik Script' },
        { value: 'ipset', label: 'Dnsmasq ipset' },
        { value: 'nfset', label: 'Dnsmasq nfset' },
        { value: 'amnezia', label: 'Amnezia' },
    ];

    const dataTypes = [
        { value: 'ipv4', label: 'IPv4 + CIDRv4' },
        { value: 'cidr4', label: 'CIDRv4' },
        { value: 'ip4', label: 'IPv4 адреса' },
        { value: 'ipv6', label: 'IPv6 + CIDRv6' },
        { value: 'cidr6', label: 'CIDRv6' },
        { value: 'ip6', label: 'IPv6 адреса' },
        { value: 'domains', label: 'Домены' },
    ];

    let runtime: Runtime = { configSet: 'main', urls: {} };
    let groups: Group[] = [];
    let loading = true;
    let query = '';
    let format = 'unifi';
    let data = 'ipv4';
    let selectionMode: SelectionMode = 'include';
    let selectedGroups = new Set<string>();
    let selectedSites = new Set<string>();
    let copied = false;
    let showAboutModal = false;
    let openSelect: SelectKey | null = null;
    const pathname = location.pathname;
    const basePath = pathname.startsWith('/beta') ? '/beta' : pathname.startsWith('/russia') ? '/russia' : '';
    const apiCategory = pathname.startsWith('/beta') ? 'beta' : pathname.startsWith('/russia') ? 'russia' : 'latest';
    const apiBase = `/api/${apiCategory}`;

    onMount(() => {
        let stopped = false;
        let timer: number | undefined;

        async function refresh() {
            await loadPortal();
            if (!stopped) {
                timer = window.setTimeout(refresh, 5000);
            }
        }

        refresh();
        return () => {
            stopped = true;
            if (timer) {
                window.clearTimeout(timer);
            }
        };
    });

    async function loadPortal() {
        const [runtimeResponse, catalogResponse] = await Promise.all([
            fetch(`${apiBase}/runtime`),
            fetch(`${apiBase}/catalog`),
        ]);
        runtime = await runtimeResponse.json();
        const catalog = await catalogResponse.json();
        groups = catalog.groups;
        loading = false;
    }

    $: normalizedQuery = query.trim().toLowerCase();
    $: filteredGroups = groups
        .map((group) => ({
            ...group,
            sites: group.sites.filter(
                (site) =>
                    !normalizedQuery ||
                    site.name.toLowerCase().includes(normalizedQuery) ||
                    group.name.toLowerCase().includes(normalizedQuery)
            ),
        }))
        .filter((group) => group.sites.length > 0);
    $: exportURL = buildExportURL(format, data, selectionMode, selectedGroups, selectedSites);
    $: downloadURL = withDownload(exportURL);
    $: downloadFilename = `iplist-${apiCategory}-${data}.${fileExtension(format)}`;
    $: totalSites = groups.reduce((sum, group) => sum + group.sites.length, 0);
    $: selectedCount = selectedGroups.size + selectedSites.size;
    $: dnsStatus = runtime.dnsRefresh;
    $: dnsProgress = dnsStatus?.total
        ? Math.max(0, Math.min(100, Math.round((dnsStatus.processed / dnsStatus.total) * 100)))
        : 0;

    function buildExportURL(
        currentFormat: string,
        currentData: string,
        currentSelectionMode: SelectionMode,
        currentGroups: Set<string>,
        currentSites: Set<string>
    ) {
        const params = new URLSearchParams({ format: currentFormat, data: currentData });
        const groupParam = currentSelectionMode === 'exclude' ? 'exclude[group]' : 'group';
        const siteParam = currentSelectionMode === 'exclude' ? 'exclude[site]' : 'site';
        for (const group of Array.from(currentGroups).sort()) {
            params.append(groupParam, group);
        }
        for (const site of Array.from(currentSites).sort()) {
            params.append(siteParam, site);
        }
        return `${apiBase}/export?${params.toString()}`;
    }

    function setSelectionMode(mode: SelectionMode) {
        selectionMode = mode;
    }

    function toggleGroup(group: Group) {
        selectedGroups = new Set(selectedGroups);
        selectedSites = new Set(selectedSites);
        if (selectedGroups.has(group.name)) {
            selectedGroups.delete(group.name);
            return;
        }
        selectedGroups.add(group.name);
        for (const site of group.sites) {
            selectedSites.delete(site.name);
        }
    }

    function toggleSite(site: Site, group: Group) {
        selectedGroups = new Set(selectedGroups);
        selectedSites = new Set(selectedSites);
        if (selectedGroups.has(group.name)) {
            selectedGroups.delete(group.name);
            for (const groupSite of group.sites) {
                if (groupSite.name !== site.name) {
                    selectedSites.add(groupSite.name);
                }
            }
            return;
        }
        if (selectedSites.has(site.name)) {
            selectedSites.delete(site.name);
        } else {
            selectedSites.add(site.name);
        }
    }

    function clearSelection() {
        selectedGroups = new Set();
        selectedSites = new Set();
    }

    function siteSelected(site: Site) {
        return selectedSites.has(site.name) || selectedGroups.has(site.group);
    }

    function selectedLabel(options: Option[], value: string) {
        return options.find((item) => item.value === value)?.label ?? value;
    }

    function toggleSelect(key: SelectKey) {
        openSelect = openSelect === key ? null : key;
    }

    async function copyURL() {
        await navigator.clipboard.writeText(`${location.origin}${exportURL}`);
        copied = true;
        setTimeout(() => (copied = false), 1200);
    }

    function withDownload(url: string) {
        return `${url}${url.includes('?') ? '&' : '?'}filesave=1`;
    }

    function fileExtension(currentFormat: string) {
        switch (currentFormat) {
            case 'amnezia':
            case 'json':
                return 'json';
            case 'mikrotik':
                return 'rsc';
            case 'ipset':
            case 'nfset':
                return 'conf';
            default:
                return 'txt';
        }
    }

    function openAbout() {
        showAboutModal = true;
    }

    function closeAbout() {
        showAboutModal = false;
    }

    function formatTime(value?: string) {
        if (!value) {
            return 'нет данных';
        }
        return new Intl.DateTimeFormat('ru-RU', {
            day: '2-digit',
            month: '2-digit',
            hour: '2-digit',
            minute: '2-digit',
        }).format(new Date(value));
    }

    const portalLinks = [
        { key: 'main', label: 'Основной', icon: 'M' },
        { key: 'beta', label: 'Бета', icon: 'β' },
        { key: 'russia', label: 'Россия', icon: 'RU' },
    ];
</script>

<svelte:window on:click={() => (openSelect = null)} />

<main class="shell" class:modal-open={showAboutModal}>
    <aside class="sidebar">
        <div class="brand">
            <img class="mark" src="/favicon.svg" alt="" />
            <div>
                <strong>iplist-go</strong>
                <span>{runtime.configSet}</span>
            </div>
        </div>

        <nav class="versions" aria-label="Версии">
            {#each portalLinks as link}
                <a
                    class:active={runtime.configSet === link.key || (runtime.configSet === 'main' && link.key === 'main')}
                    href={runtime.urls[link.key] || '/'}
                >
                    <span class="version-icon">{link.icon}</span>
                    {link.label}
                </a>
            {/each}
        </nav>

        {#if dnsStatus}
            <section class="dns-panel">
                <span class="eyebrow">DNS refresh</span>
                {#if !dnsStatus.enabled}
                    <strong>Отключено</strong>
                    <p>Локальный DNS-refresh выключен.</p>
                {:else if dnsStatus.running}
                    <strong>Обновление {dnsStatus.processed}/{dnsStatus.total}</strong>
                    <p>{dnsStatus.currentSite || 'подготовка'}</p>
                    <p>найдено {dnsStatus.resolved} · добавлено {dnsStatus.added}</p>
                    <div class="dns-progress" aria-label="DNS refresh progress">
                        <span style={`width: ${dnsProgress}%`}></span>
                    </div>
                {:else}
                    <strong>Ожидание цикла</strong>
                    <p>Последнее: {formatTime(dnsStatus.lastFinishedAt)}</p>
                    <p>Следующее: {formatTime(dnsStatus.nextRunAt)}</p>
                    <p>Добавлено: {dnsStatus.added} записей</p>
                {/if}
                {#if dnsStatus.lastError}
                    <p class="error">{dnsStatus.lastError}</p>
                {/if}
            </section>
        {/if}

        <nav class="meta" aria-label="Ссылки">
            <button class="about-button" type="button" on:click={openAbout} aria-label="О проекте" title="О проекте">
                <svg viewBox="0 0 24 24" aria-hidden="true">
                    <circle cx="12" cy="12" r="9" />
                    <path d="M12 11v5" />
                    <path d="M12 8h.01" />
                </svg>
                <span>О проекте</span>
            </button>
        </nav>
    </aside>

    <section class="workspace">
        <div class:page-blurred={showAboutModal}>
            <header class="topbar">
            <div>
                <h1>Списки адресов</h1>
                <p>{totalSites} сервисов в {groups.length} группах</p>
            </div>
        </header>

            <section class="controls">
                <label>
                    <span>Поиск</span>
                    <input bind:value={query} placeholder="service, group, domain..." />
                </label>
                <label>
                    <span>Режим выбора</span>
                    <div class="mode-toggle">
                        <button
                            type="button"
                            class:active={selectionMode === 'include'}
                            on:click={() => setSelectionMode('include')}
                        >
                            Include
                        </button>
                        <button
                            type="button"
                            class:active={selectionMode === 'exclude'}
                            on:click={() => setSelectionMode('exclude')}
                        >
                            Exclude
                        </button>
                    </div>
                </label>
                <label>
                    <span>Формат</span>
                    <div class="select-shell">
                        <button
                            type="button"
                            class="select-trigger"
                            class:open={openSelect === 'format'}
                            on:click|stopPropagation={() => toggleSelect('format')}
                        >
                            <span>{selectedLabel(formats, format)}</span>
                            <span class="select-arrow">⌄</span>
                        </button>
                        {#if openSelect === 'format'}
                            <div class="select-menu">
                                {#each formats as item}
                                    <button
                                        type="button"
                                        class:selected={format === item.value}
                                        on:click|stopPropagation={() => {
                                            format = item.value;
                                            openSelect = null;
                                        }}
                                    >
                                        {item.label}
                                    </button>
                                {/each}
                            </div>
                        {/if}
                    </div>
                </label>
                <label>
                    <span>Тип данных</span>
                    <div class="select-shell">
                        <button
                            type="button"
                            class="select-trigger"
                            class:open={openSelect === 'data'}
                            on:click|stopPropagation={() => toggleSelect('data')}
                        >
                            <span>{selectedLabel(dataTypes, data)}</span>
                            <span class="select-arrow">⌄</span>
                        </button>
                        {#if openSelect === 'data'}
                            <div class="select-menu">
                                {#each dataTypes as item}
                                    <button
                                        type="button"
                                        class:selected={data === item.value}
                                        on:click|stopPropagation={() => {
                                            data = item.value;
                                            openSelect = null;
                                        }}
                                    >
                                        {item.label}
                                    </button>
                                {/each}
                            </div>
                        {/if}
                    </div>
                </label>
            </section>

            <section class="export-card">
                <div class="export-url">
                    <span class="eyebrow">Export URL</span>
                    <code>{exportURL}</code>
                </div>
                <div class="export-actions" aria-label="Export actions">
                    <button
                        type="button"
                        class="icon-action"
                        class:copied
                        on:click={copyURL}
                        aria-label={copied ? 'Скопировано' : 'Копировать ссылку'}
                        title={copied ? 'Скопировано' : 'Копировать'}
                    >
                        {#if copied}
                            <svg viewBox="0 0 24 24" aria-hidden="true">
                                <path d="M20 6 9 17l-5-5" />
                            </svg>
                        {:else}
                            <svg viewBox="0 0 24 24" aria-hidden="true">
                                <rect x="9" y="9" width="10" height="10" rx="2" />
                                <path d="M5 15H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h8a2 2 0 0 1 2 2v1" />
                            </svg>
                        {/if}
                    </button>
                    <a
                        class="icon-action"
                        href={downloadURL}
                        download={downloadFilename}
                        aria-label="Скачать файл"
                        title="Скачать"
                    >
                        <svg viewBox="0 0 24 24" aria-hidden="true">
                            <path d="M12 3v12" />
                            <path d="m7 10 5 5 5-5" />
                            <path d="M5 21h14" />
                        </svg>
                    </a>
                    <a
                        class="icon-action"
                        href={exportURL}
                        target="_blank"
                        rel="noreferrer"
                        aria-label="Открыть экспорт"
                        title="Открыть"
                    >
                        <svg viewBox="0 0 24 24" aria-hidden="true">
                            <circle cx="12" cy="12" r="9" />
                            <path d="M3 12h18" />
                            <path d="M12 3a14 14 0 0 1 0 18" />
                            <path d="M12 3a14 14 0 0 0 0 18" />
                        </svg>
                    </a>
                </div>
                <div class="actions">
                    {#if selectedCount > 0}
                        <button class="ghost" on:click={clearSelection}>Сбросить: {selectedCount}</button>
                    {/if}
                </div>
            </section>

            {#if loading}
                <div class="state">Загрузка конфигов...</div>
            {:else}
                <section class="groups">
                    {#each filteredGroups as group}
                        <article class="group">
                            <div class="group-title">
                                <button
                                    type="button"
                                    class="group-toggle"
                                    class:selected={selectedGroups.has(group.name)}
                                    on:click={() => toggleGroup(group)}
                                >
                                    <span class="checkmark">{selectedGroups.has(group.name) ? '✓' : ''}</span>
                                    <h2>{group.name}</h2>
                                </button>
                                <span>{group.sites.length}</span>
                            </div>
                            <div class="sites">
                                {#each group.sites as site}
                                    <button
                                        type="button"
                                        class:selected={siteSelected(site)}
                                        class:group-selected={selectedGroups.has(group.name)}
                                        class="site"
                                        on:click={() => toggleSite(site, group)}
                                        title={site.name}
                                    >
                                        <img src={site.icon} alt="" loading="lazy" />
                                        <span>{site.name}</span>
                                        {#if site.dynamicTotal}
                                            <strong class="delta">+{site.dynamicTotal}</strong>
                                        {/if}
                                    </button>
                                {/each}
                            </div>
                        </article>
                    {/each}
                </section>
            {/if}
        </div>
    </section>

    {#if showAboutModal}
        <div class="modal-layer">
            <button
                class="modal-backdrop"
                type="button"
                on:click={closeAbout}
                aria-label="Закрыть окно О проекте"
            ></button>
            <div
                class="about-modal"
                role="dialog"
                aria-modal="true"
                aria-labelledby="about-title"
            >
                <button class="modal-close" type="button" on:click={closeAbout} aria-label="Закрыть">
                    <svg viewBox="0 0 24 24" aria-hidden="true">
                        <path d="m6 6 12 12" />
                        <path d="m18 6-12 12" />
                    </svg>
                </button>
                <h2 id="about-title">О проекте</h2>
                <p>
                    Данный сервис предназначен для сбора и обновления IP-адресов IPv4 и IPv6, а также их CIDR-зон для указанных доменов.
                </p>
                <p>
                    Вы сами можете найти подходящее применение данному сервису по своему усмотрению.
                </p>
                <p>
                    Идея и исходная реализация принадлежат проекту
                    <a class="repo-link" href="https://github.com/rekryt/iplist" target="_blank" rel="noreferrer">
                        <svg viewBox="0 0 24 24" aria-hidden="true">
                            <path d="M12 .5a12 12 0 0 0-3.8 23.4c.6.1.8-.2.8-.6v-2.1c-3.3.7-4-1.4-4-1.4-.5-1.3-1.3-1.7-1.3-1.7-1.1-.7.1-.7.1-.7 1.2.1 1.8 1.2 1.8 1.2 1.1 1.8 2.8 1.3 3.5 1 .1-.8.4-1.3.8-1.6-2.6-.3-5.4-1.3-5.4-5.9 0-1.3.5-2.4 1.2-3.2-.1-.3-.5-1.6.1-3.2 0 0 1-.3 3.3 1.2a11.4 11.4 0 0 1 6 0c2.3-1.5 3.3-1.2 3.3-1.2.6 1.6.2 2.9.1 3.2.8.8 1.2 1.9 1.2 3.2 0 4.6-2.8 5.6-5.4 5.9.4.4.8 1.1.8 2.2v3.2c0 .4.2.7.8.6A12 12 0 0 0 12 .5Z" />
                        </svg>
                        rekryt/iplist
                    </a>.
                    Этот форк
                    <a class="repo-link" href="https://github.com/dexogen/iplist-go" target="_blank" rel="noreferrer">
                        <svg viewBox="0 0 24 24" aria-hidden="true">
                            <path d="M12 .5a12 12 0 0 0-3.8 23.4c.6.1.8-.2.8-.6v-2.1c-3.3.7-4-1.4-4-1.4-.5-1.3-1.3-1.7-1.3-1.7-1.1-.7.1-.7.1-.7 1.2.1 1.8 1.2 1.8 1.2 1.1 1.8 2.8 1.3 3.5 1 .1-.8.4-1.3.8-1.6-2.6-.3-5.4-1.3-5.4-5.9 0-1.3.5-2.4 1.2-3.2-.1-.3-.5-1.6.1-3.2 0 0 1-.3 3.3 1.2a11.4 11.4 0 0 1 6 0c2.3-1.5 3.3-1.2 3.3-1.2.6 1.6.2 2.9.1 3.2.8.8 1.2 1.9 1.2 3.2 0 4.6-2.8 5.6-5.4 5.9.4.4.8 1.1.8 2.2v3.2c0 .4.2.7.8.6A12 12 0 0 0 12 .5Z" />
                        </svg>
                        dexogen/iplist-go
                    </a>
                    использует совместимые JSON-конфиги оригинального проекта, но интерфейс и API написаны с нуля на Go + Svelte.
                </p>
            </div>
        </div>
    {/if}
</main>
