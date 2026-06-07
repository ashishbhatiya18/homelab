'use client';

import { useState, useEffect } from 'react';
import { stacksAPI } from '@/lib/api';
import { Stack } from '@/types';

export function useStacks() {
  const [stacks, setStacks] = useState<Stack[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const fetchStacks = async () => {
      try {
        const response = await stacksAPI.listStacks();
        setStacks(response.data);
        setError(null);
      } catch (err) {
        setError(err instanceof Error ? err.message : 'Failed to fetch stacks');
      } finally {
        setLoading(false);
      }
    };

    fetchStacks();
    const interval = setInterval(fetchStacks, 30000); // Refresh every 30 seconds
    return () => clearInterval(interval);
  }, []);

  return { stacks, loading, error };
}
