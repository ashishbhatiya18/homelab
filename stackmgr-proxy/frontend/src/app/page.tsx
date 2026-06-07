'use client';

import { useStacks } from '@/lib/hooks';
import StackCard from '@/components/StackCard';
import SystemStatus from '@/components/SystemStatus';

export default function Home() {
  const { stacks, loading, error } = useStacks();

  const groupedByNode = stacks.reduce(
    (acc, stack) => {
      if (!acc[stack.node]) acc[stack.node] = [];
      acc[stack.node].push(stack);
      return acc;
    },
    {} as Record<string, typeof stacks>
  );

  return (
    <div className="min-h-screen bg-slate-950">
      <header className="border-b border-slate-800 bg-slate-900/50 backdrop-blur-sm sticky top-0 z-40">
        <div className="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-4">
          <h1 className="text-2xl font-bold text-white">StackMgr</h1>
          <p className="text-slate-400 text-sm">LocalStack Management Dashboard</p>
        </div>
      </header>

      <main className="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-8">
        <SystemStatus />

        {error && (
          <div className="mb-8 p-4 bg-red-500/10 border border-red-500/30 rounded-lg text-red-400">
            {error}
          </div>
        )}

        {loading ? (
          <div className="text-center py-12">
            <div className="inline-block animate-spin rounded-full h-8 w-8 border-b-2 border-blue-500" />
            <p className="mt-4 text-slate-400">Loading stacks…</p>
          </div>
        ) : (
          <div className="space-y-10">
            {Object.entries(groupedByNode).map(([node, nodeStacks]) => (
              <section key={node}>
                <div className="flex items-center gap-3 mb-4">
                  <h2 className="text-sm font-semibold text-slate-400 uppercase tracking-wider">{node}</h2>
                  <div className="flex-1 h-px bg-slate-800" />
                  <span className="text-xs text-slate-600">{nodeStacks.length} stacks</span>
                </div>
                <div className="flex flex-col gap-2">
                  {nodeStacks.map((stack) => (
                    <StackCard key={`${stack.node}-${stack.name}`} stack={stack} />
                  ))}
                </div>
              </section>
            ))}
          </div>
        )}
      </main>
    </div>
  );
}
