import { useCallback, useEffect, useRef, useState, type DependencyList } from "react";

interface ResourceState<T> {
  data: T | undefined;
  error: unknown;
  loading: boolean;
  refreshing: boolean;
}

export interface Resource<T> extends ResourceState<T> {
  reload: () => void;
  setData: React.Dispatch<React.SetStateAction<T | undefined>>;
}

export function useResource<T>(loader: (signal: AbortSignal) => Promise<T>, dependencies: DependencyList): Resource<T> {
  const loaderRef = useRef(loader);
  loaderRef.current = loader;
  const [reloadToken, setReloadToken] = useState(0);
  const [state, setState] = useState<ResourceState<T>>({
    data: undefined,
    error: undefined,
    loading: true,
    refreshing: false
  });

  useEffect(() => {
    const controller = new AbortController();
    setState((current) => ({
      ...current,
      error: undefined,
      loading: current.data === undefined,
      refreshing: current.data !== undefined
    }));
    loaderRef.current(controller.signal).then(
      (data) => {
        if (!controller.signal.aborted) setState({ data, error: undefined, loading: false, refreshing: false });
      },
      (error: unknown) => {
        if (!controller.signal.aborted) {
          setState((current) => ({ ...current, error, loading: false, refreshing: false }));
        }
      }
    );
    return () => controller.abort();
  }, [...dependencies, reloadToken]);

  const reload = useCallback(() => setReloadToken((value) => value + 1), []);
  const setData = useCallback<React.Dispatch<React.SetStateAction<T | undefined>>>((update) => {
    setState((current) => ({
      ...current,
      data: typeof update === "function" ? (update as (value: T | undefined) => T | undefined)(current.data) : update
    }));
  }, []);

  return { ...state, reload, setData };
}
