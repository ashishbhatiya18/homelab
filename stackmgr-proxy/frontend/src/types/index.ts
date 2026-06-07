export interface Stack {
  node: string;
  environment: string;
  name: string;
  path: string;
  status: string;
  services: Service[];
}

export interface Service {
  name: string;
  status: string;
  state: string;
  url?: string;
}

export interface StackDetail {
  node: string;
  name: string;
  status: string;
  services: Service[];
}

export interface HealthCheckResult {
  service: string;
  status: string;
  message: string;
  code: number;
}

export interface SystemHealth {
  status: string;
  services: number;
  healthy: number;
  message: string;
}
