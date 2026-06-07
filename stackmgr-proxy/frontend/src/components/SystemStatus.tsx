'use client';

import { useState, useEffect } from 'react';
import { stacksAPI } from '@/lib/api';
import { SystemHealth } from '@/types';

export default function SystemStatus() {
  const [health, setHealth] = useState<SystemHealth | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    const fetchHealth = async () => {
      try {
        const response = await stacksAPI.getHealth();
        setHealth(response.data);
      } catch (error) {
        console.error('Failed to fetch health:', error);
      } finally {
        setLoading(false);
      }
    };

    fetchHealth();
    const interval = setInterval(fetchHealth, 60000); // Refresh every minute
    return () => clearInterval(interval);
  }, []);

  if (loading || !health) {
    return (
      <div className="card mb-8 animate-pulse">
        <div className="h-20 bg-slate-800 rounded"></div>
      </div>
    );
  }

  const healthPercentage = health.services > 0 ? (health.healthy / health.services) * 100 : 0;
  const isHealthy = health.status === 'healthy';

  return (
    <div className="card mb-8">
      <div className="space-y-4">
        <div className="flex items-center justify-between">
          <h2 className="text-xl font-bold text-white">System Status</h2>
          <span className={`${isHealthy ? 'badge-success' : 'badge-error'} capitalize`}>
            {health.status}
          </span>
        </div>

        <p className="text-slate-400">{health.message}</p>

        <div className="grid grid-cols-2 gap-4">
          <div>
            <p className="text-sm text-slate-400 mb-1">Total Services</p>
            <p className="text-3xl font-bold text-white">{health.services}</p>
          </div>
          <div>
            <p className="text-sm text-slate-400 mb-1">Healthy</p>
            <p className="text-3xl font-bold text-green-400">{health.healthy}</p>
          </div>
        </div>

        {/* Health Bar */}
        <div className="space-y-2">
          <div className="flex justify-between text-sm">
            <span className="text-slate-400">Health</span>
            <span className="text-slate-300">{healthPercentage.toFixed(0)}%</span>
          </div>
          <div className="w-full h-2 bg-slate-800 rounded-full overflow-hidden">
            <div
              className={`h-full rounded-full transition-all ${
                healthPercentage === 100
                  ? 'bg-green-500'
                  : healthPercentage >= 75
                    ? 'bg-yellow-500'
                    : 'bg-red-500'
              }`}
              style={{ width: `${healthPercentage}%` }}
            ></div>
          </div>
        </div>
      </div>
    </div>
  );
}
