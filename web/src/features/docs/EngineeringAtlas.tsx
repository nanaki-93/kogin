// Engineering Atlas: deep Computer Science, Distributed Systems, Storage,
// Architecture, Security, and Go Systems mental models connected to the active
// codebase's Learning log, with offline Socratic self-checks.

import { useEffect, useMemo, useState } from 'react';
import { AlertTriangle, BookOpen, CheckCircle2, Compass, GraduationCap, HelpCircle, MessageCircleQuestion, Search } from 'lucide-react';
import { useLessons } from '@/api/hooks';
import {
  ATLAS_DOMAINS,
  allCuratedConcepts,
  computeAtlasMastery,
  type AtlasDomain,
  type ConceptGuide,
} from '@/lib/concepts';
import { highlightLines } from '@/lib/highlight';
import { navigate, useRoute } from '@/lib/router';
import { plural } from '@/lib/util';
import { Badge, Button, EmptyState, Input, Progress, Segmented, cx } from '@/components/ui';
import { askConceptInProject, CATEGORY_TONE } from './ConceptChip';

const MASTERED_STORAGE_KEY = 'kogin.atlas.mastered.v1';

type AtlasScope = 'all' | 'met' | 'practice';

function loadMasteredIds(): Set<string> {
  try {
    const raw = localStorage.getItem(MASTERED_STORAGE_KEY);
    if (!raw) return new Set();
    const parsed = JSON.parse(raw);
    return Array.isArray(parsed) ? new Set(parsed.filter((v): v is string => typeof v === 'string')) : new Set();
  } catch {
    return new Set();
  }
}

function saveMasteredIds(ids: ReadonlySet<string>): void {
  try {
    localStorage.setItem(MASTERED_STORAGE_KEY, JSON.stringify([...ids]));
  } catch {
    // Ignore storage quota or private-browsing failures.
  }
}

export function EngineeringAtlas() {
  const route = useRoute();
  const targetConcept = route.params.get('concept') ?? '';
  const targetDomain = route.params.get('domain') ?? '';
  const lessons = useLessons().data?.lessons ?? [];
  const mastery = useMemo(() => computeAtlasMastery(lessons), [lessons]);
  const concepts = useMemo(() => allCuratedConcepts(), []);

  const [domainFilter, setDomainFilter] = useState<AtlasDomain | ''>(() =>
    ATLAS_DOMAINS.some(d => d.id === targetDomain) ? (targetDomain as AtlasDomain) : '',
  );
  const [scope, setScope] = useState<AtlasScope>('all');
  const [query, setQuery] = useState('');
  const [mastered, setMastered] = useState<Set<string>>(() => loadMasteredIds());

  useEffect(() => {
    if (ATLAS_DOMAINS.some(d => d.id === targetDomain)) setDomainFilter(targetDomain as AtlasDomain);
  }, [targetDomain]);

  useEffect(() => {
    if (!targetConcept) return;
    setDomainFilter('');
    setScope('all');
    setQuery('');
    const frame = requestAnimationFrame(() => {
      document.getElementById(`atlas-${targetConcept}`)?.scrollIntoView({ block: 'center', behavior: 'smooth' });
    });
    return () => cancelAnimationFrame(frame);
  }, [targetConcept]);

  const toggleMastered = (id: string) => {
    setMastered(prev => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      saveMasteredIds(next);
      return next;
    });
  };

  const filtered = useMemo(() => {
    const needle = query.trim().toLowerCase();
    return concepts.filter(entry => {
      if (domainFilter && entry.domain !== domainFilter) return false;
      const seenCount = mastery.countsById.get(entry.id) ?? 0;
      if (scope === 'met' && seenCount === 0) return false;
      if (scope === 'practice' && mastered.has(entry.id)) return false;
      if (!needle) return true;
      const hay = `${entry.title} ${entry.domain} ${entry.category} ${entry.what} ${entry.mentalModel} ${entry.inGo} ${entry.tradeOffs ?? ''} ${entry.failureModes ?? ''}`.toLowerCase();
      return hay.includes(needle);
    });
  }, [concepts, domainFilter, mastered, mastery.countsById, query, scope]);

  const activeDomainInfo = ATLAS_DOMAINS.find(d => d.id === domainFilter);
  const masteredOrMetCount = useMemo(
    () => concepts.filter(c => mastered.has(c.id) || (mastery.countsById.get(c.id) ?? 0) > 0).length,
    [concepts, mastered, mastery.countsById],
  );

  return (
    <article className="doc atlas-page" aria-label="Engineering Atlas">
      <header className="doc-head atlas-hero">
        <div className="row">
          <Badge tone="accent" icon={Compass}>CS & System Design Studio</Badge>
          <span className="xs muted">
            {mastery.metCurated} met in this repo · {mastered.size} Socratic checks mastered · {concepts.length} core models
          </span>
        </div>
        <h1>Engineering Atlas</h1>
        <p className="lead">
          First-principles mental models across concurrency, distributed systems, storage engines, algorithms, architecture, and security—paired with Socratic checks and 1-click codebase tracing.
        </p>
        <div className="atlas-overall-bar">
          <div className="row xs">
            <span className="strong">Curriculum coverage</span>
            <span className="spacer" />
            <span className="muted mono">{masteredOrMetCount} / {concepts.length} explored</span>
          </div>
          <Progress value={(masteredOrMetCount / Math.max(1, concepts.length)) * 100} tone="accent" label="Engineering Atlas coverage" />
        </div>
      </header>

      <section className="atlas-domains" aria-label="Engineering domains">
        {mastery.domains.map(({ domain, concepts: domainConcepts, metCount, totalCount }) => {
          const domainDone = domainConcepts.filter(c => mastered.has(c.id) || (mastery.countsById.get(c.id) ?? 0) > 0).length;
          const active = domainFilter === domain.id;
          return (
            <button
              key={domain.id}
              type="button"
              className={cx('atlas-domain-card', active && 'is-active')}
              aria-pressed={active}
              onClick={() => setDomainFilter(active ? '' : domain.id)}
            >
              <div className="row">
                <span className="strong small truncate">{domain.short}</span>
                <span className="spacer" />
                <span className="xs mono faint">{domainDone}/{totalCount}</span>
              </div>
              <p className="xs muted atlas-domain-focus">{domain.focus}</p>
              <Progress value={(domainDone / Math.max(1, totalCount)) * 100} tone={domainDone === totalCount ? 'success' : 'accent'} label={`${domain.id} progress`} />
              {metCount > 0 ? <span className="xs tone-accent">{metCount} met in project</span> : <span className="xs faint">Not met in log yet</span>}
            </button>
          );
        })}
      </section>

      {activeDomainInfo ? (
        <div className="atlas-domain-banner">
          <div>
            <div className="strong small">{activeDomainInfo.id}</div>
            <div className="xs muted">{activeDomainInfo.summary}</div>
          </div>
          <Button size="sm" variant="ghost" onClick={() => setDomainFilter('')}>All 7 domains</Button>
        </div>
      ) : null}

      <div className="atlas-toolbar">
        <Input
          icon={Search}
          value={query}
          onChange={e => setQuery(e.target.value)}
          placeholder="Filter by concept, invariant, complexity, or failure mode…"
          aria-label="Filter Atlas concepts"
          className="grow"
        />
        <Segmented
          label="Atlas filter"
          value={scope}
          onChange={setScope}
          options={[
            { value: 'all', label: 'All', count: concepts.length },
            { value: 'met', label: 'Met in repo', count: mastery.metCurated },
            { value: 'practice', label: 'To practice', count: concepts.length - mastered.size },
          ]}
        />
      </div>

      {!filtered.length ? (
        <EmptyState
          icon={GraduationCap}
          title="No matching concepts"
          action={
            <Button variant="secondary" onClick={() => { setQuery(''); setDomainFilter(''); setScope('all'); }}>
              Reset filters
            </Button>
          }
        >
          No curriculum cards match the current filter. Clear the filter to browse all {concepts.length} computer science and system design models.
        </EmptyState>
      ) : (
        <div className="atlas-list" role="list" aria-label="Curriculum concepts">
          {filtered.map(entry => (
            <AtlasConceptCard
              key={entry.id}
              guide={entry}
              highlighted={entry.id === targetConcept}
              seenCount={mastery.countsById.get(entry.id) ?? 0}
              projectTag={mastery.tagsById.get(entry.id)}
              isMastered={mastered.has(entry.id)}
              onToggleMastered={() => toggleMastered(entry.id)}
            />
          ))}
        </div>
      )}
    </article>
  );
}

function AtlasConceptCard({
  guide,
  highlighted,
  seenCount,
  projectTag,
  isMastered,
  onToggleMastered,
}: {
  guide: ConceptGuide;
  highlighted: boolean;
  seenCount: number;
  projectTag?: string;
  isMastered: boolean;
  onToggleMastered: () => void;
}) {
  const [showAnswer, setShowAnswer] = useState(false);
  const codeLines = useMemo(() => (guide.code ? highlightLines(guide.code, 'go') : []), [guide.code]);

  return (
    <section
      id={`atlas-${guide.id}`}
      role="listitem"
      className={cx('atlas-card', highlighted && 'is-targeted', isMastered && 'is-mastered')}
      aria-label={guide.title}
    >
      <div className="atlas-card-head">
        <Badge tone={CATEGORY_TONE[guide.category]}>{guide.category}</Badge>
        <Badge tone="neutral">{guide.domain}</Badge>
        {seenCount > 0 ? (
          <Badge tone="accent" title="Recorded in accepted tasks in your Learning log">
            Met {plural(seenCount, 'time')} in repo
          </Badge>
        ) : null}
        <span className="spacer" />
        <button
          type="button"
          className={cx('chip', 'atlas-master-btn', isMastered && 'is-on')}
          aria-pressed={isMastered}
          onClick={onToggleMastered}
          title="Mark that you can reason through and apply this mental model from first principles"
        >
          <CheckCircle2 size={12} aria-hidden />
          <span>{isMastered ? 'Mastered' : 'Mark mastered'}</span>
        </button>
      </div>

      <h2 className="atlas-card-title">{guide.title}</h2>
      <p className="small">{guide.what}</p>

      <div className="atlas-mental-model small">
        <span className="strong">Senior mental model · </span>
        {guide.mentalModel}
      </div>

      <div className="atlas-specs">
        {guide.complexity ? (
          <div className="atlas-spec">
            <div className="atlas-spec-label">Complexity & Systems Cost</div>
            <div className="xs">{guide.complexity}</div>
          </div>
        ) : null}
        {guide.tradeOffs ? (
          <div className="atlas-spec">
            <div className="atlas-spec-label">Design Trade-offs</div>
            <div className="xs">{guide.tradeOffs}</div>
          </div>
        ) : null}
        {guide.failureModes ? (
          <div className="atlas-spec">
            <div className="atlas-spec-label">Production Failure Modes</div>
            <div className="xs">{guide.failureModes}</div>
          </div>
        ) : null}
        <div className="atlas-spec">
          <div className="atlas-spec-label">In Go & Systems Practice</div>
          <div className="xs">{guide.inGo}</div>
        </div>
      </div>

      {codeLines.length ? (
        <pre className="concept-code" aria-label={`Example for ${guide.title}`}>
          {codeLines.map((html, idx) => (
            <code key={idx} dangerouslySetInnerHTML={{ __html: html || ' ' }} />
          ))}
        </pre>
      ) : null}

      {guide.watchOut ? (
        <div className="concept-watch xs">
          <AlertTriangle size={13} className="tone-warning" aria-hidden />
          <span>{guide.watchOut}</span>
        </div>
      ) : null}

      {guide.selfCheck ? (
        <div className="atlas-socratic" role="region" aria-label={`Socratic check for ${guide.title}`}>
          <div className="row xs">
            <HelpCircle size={13} className="tone-accent" aria-hidden />
            <span className="strong">Socratic self-check</span>
            <span className="spacer" />
            <button
              type="button"
              className="link-inline xs"
              aria-expanded={showAnswer}
              onClick={() => setShowAnswer(open => !open)}
            >
              {showAnswer ? 'Hide answer' : 'Reveal senior answer'}
            </button>
          </div>
          <p className="xs atlas-socratic-q">{guide.selfCheck.prompt}</p>
          {showAnswer ? <p className="xs atlas-socratic-a">{guide.selfCheck.answer}</p> : null}
        </div>
      ) : null}

      <div className="atlas-card-foot">
        <Button
          size="sm"
          variant="secondary"
          icon={MessageCircleQuestion}
          onClick={() => askConceptInProject(projectTag ?? guide.title)}
          title="Open Ask the code pre-filled with a question tracing this concept in the repository"
        >
          Trace in this codebase
        </Button>
        {seenCount > 0 ? (
          <Button size="sm" variant="ghost" icon={BookOpen} onClick={() => navigate('/docs/log')}>
            View in Learning log
          </Button>
        ) : null}
      </div>
    </section>
  );
}
