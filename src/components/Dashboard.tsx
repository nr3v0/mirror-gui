import { useState, useEffect } from 'react';
import { useNavigate } from 'react-router-dom';
import axios from 'axios';
import {
  PageSection,
  Card,
  CardBody,
  CardTitle,
  CardHeader,
  Grid,
  GridItem,
  Label,
  Title,
  Button,
  Spinner,
  Popover,
  DescriptionList,
  DescriptionListGroup,
  DescriptionListTerm,
  DescriptionListDescription,
  Alert,
  EmptyState,
  EmptyStateBody,
  Timestamp,
} from '@patternfly/react-core';
import {
  SyncAltIcon,
  HistoryIcon,
  ListIcon,
  ServerIcon,
  KeyIcon,
  InfoCircleIcon,
  OutlinedClockIcon,
} from '@patternfly/react-icons';
import { Table, Thead, Tbody, Tr, Th, Td } from '@patternfly/react-table';
import { useAlerts } from '../AlertContext';

interface Stats {
  totalOperations: number;
  successfulOperations: number;
  failedOperations: number;
  runningOperations: number;
}

interface Operation {
  name: string;
  configFile: string;
  status: string;
  startedAt: string;
  duration: number | null;
}

interface SystemStatus {
  ocMirrorVersion: string;
  systemHealth: string;
  pullSecretDetected: boolean;
}

interface SystemInfo {
  ocMirrorSource?: string;
  availableDiskSpace: number;
  totalDiskSpace: number;
  systemArchitecture: string;
  cacheDir: string;
  hostCacheDir: string;
  cacheSizeBytes: number;
}

// Where the active oc-mirror binary came from (ocMirrorSource in /api/system/info).
const OC_MIRROR_SOURCE_LABELS: Record<string, string> = {
  source: 'Built from source',
  redhat: 'Red Hat build',
  'redhat-runtime': 'Red Hat build (downloaded)',
};

type LabelStatus = 'success' | 'warning' | 'danger' | 'info' | 'custom';

const getStatusLabelStatus = (status: string): LabelStatus => {
  switch (status) {
    case 'healthy':
      return 'success';
    case 'degraded':
    case 'warning':
      return 'warning';
    case 'error':
      return 'danger';
    case 'running':
      return 'custom';
    default:
      return 'custom';
  }
};

const getStatusText = (status: string): string => {
  switch (status) {
    case 'healthy':
      return 'Healthy';
    case 'degraded':
      return 'Low Disk Space';
    case 'warning':
      return 'Warning';
    case 'error':
      return 'Error';
    case 'running':
      return 'Running';
    default:
      return 'Unknown';
  }
};

const getOperationLabelStatus = (status: string): LabelStatus => {
  switch (status) {
    case 'success':
      return 'success';
    case 'running':
      return 'custom';
    case 'failed':
      return 'danger';
    case 'stopped':
      return 'warning';
    default:
      return 'custom';
  }
};

const getOperationStatusText = (status: string): string => {
  switch (status) {
    case 'success':
      return 'Success';
    case 'running':
      return 'Running';
    case 'failed':
      return 'Failed';
    case 'stopped':
      return 'Stopped';
    default:
      return 'Unknown';
  }
};

const formatDuration = (seconds?: number) => {
  if (!seconds) return '-';
  const hours = Math.floor(seconds / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);
  const secs = seconds % 60;
  if (hours > 0) return `${hours}h ${minutes}m ${secs}s`;
  if (minutes > 0) return `${minutes}m ${secs}s`;
  return `${secs}s`;
};

const Dashboard: React.FC = () => {
  const { addDangerAlert } = useAlerts();
  const navigate = useNavigate();

  const [stats, setStats] = useState<Stats>({
    totalOperations: 0,
    successfulOperations: 0,
    failedOperations: 0,
    runningOperations: 0,
  });
  const [recentOperations, setRecentOperations] = useState<Operation[]>([]);
  const [systemStatus, setSystemStatus] = useState<SystemStatus>({
    ocMirrorVersion: '',
    systemHealth: 'unknown',
    pullSecretDetected: true,
  });
  const [systemInfo, setSystemInfo] = useState<SystemInfo>({
    availableDiskSpace: 0,
    totalDiskSpace: 0,
    systemArchitecture: '',
    cacheDir: '',
    hostCacheDir: '',
    cacheSizeBytes: 0,
  });
  const [loading, setLoading] = useState(true);

  const formatBytes = (bytes: number): string => {
    if (!bytes) return 'Unknown';
    const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
    const i = Math.floor(Math.log(bytes) / Math.log(1024));
    return `${(bytes / Math.pow(1024, i)).toFixed(2)} ${sizes[i]}`;
  };

  const fetchDashboardData = async () => {
    try {
      const [statsRes, operationsRes, statusRes, infoRes] = await Promise.all([
        axios.get('/api/stats'),
        axios.get('/api/operations/recent'),
        axios.get('/api/system/status'),
        axios.get('/api/system/info'),
      ]);
      setStats(statsRes.data);
      setRecentOperations(operationsRes.data);
      setSystemStatus(statusRes.data);
      setSystemInfo(infoRes.data);
    } catch (error) {
      console.error('Error fetching dashboard data:', error);
      addDangerAlert('Failed to load dashboard data');
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    fetchDashboardData();
    const interval = setInterval(fetchDashboardData, 30000);
    return () => clearInterval(interval);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);



  if (loading) {
    return (
      <PageSection>
        <div className="pf-v6-u-text-align-center pf-v6-u-p-2xl">
          <Spinner aria-label="Loading dashboard" />
          <Title headingLevel="h3" className="pf-v6-u-mt-md">
            Loading dashboard...
          </Title>
        </div>
      </PageSection>
    );
  }

  return (
    <>
      {!systemStatus.pullSecretDetected && (
        <PageSection>
          <Alert
            variant="warning"
            isInline
            title="No pull secret detected"
            actionLinks={
              <Button variant="link" onClick={() => navigate('/settings?tab=pull-secret')}>
                Go to Settings
              </Button>
            }
          >
            Mirroring operations will not be available. Provide a pull secret in Settings &gt; Pull Secret.
          </Alert>
        </PageSection>
      )}

      {/* Environment */}
      <PageSection>
        <Card>
          <CardHeader>
            <CardTitle>
              <Title headingLevel="h2">
                <ServerIcon className="pf-v6-u-mr-sm" />
                Environment
              </Title>
            </CardTitle>
          </CardHeader>
          <CardBody>
            <DescriptionList isCompact columnModifier={{ default: '3Col' }}>
              <DescriptionListGroup>
                <DescriptionListTerm>
                  <SyncAltIcon className="pf-v6-u-mr-sm" />
                  OC Mirror Version
                </DescriptionListTerm>
                <DescriptionListDescription>
                  {systemStatus.ocMirrorVersion || 'Not available'}
                  {OC_MIRROR_SOURCE_LABELS[systemInfo.ocMirrorSource ?? ''] && (
                    <Label isCompact color="grey" className="pf-v6-u-ml-sm">
                      {OC_MIRROR_SOURCE_LABELS[systemInfo.ocMirrorSource ?? '']}
                    </Label>
                  )}
                </DescriptionListDescription>
              </DescriptionListGroup>
              <DescriptionListGroup>
                <DescriptionListTerm>
                  Environment Status
                  <Popover
                    position="right"
                    headerContent="Environment Details"
                    headerIcon={<InfoCircleIcon />}
                    bodyContent={
                      <DescriptionList isCompact>
                        <DescriptionListGroup>
                          <DescriptionListTerm>Architecture</DescriptionListTerm>
                          <DescriptionListDescription>{systemInfo.systemArchitecture || 'Unknown'}</DescriptionListDescription>
                        </DescriptionListGroup>
                        <DescriptionListGroup>
                          <DescriptionListTerm>Disk Available</DescriptionListTerm>
                          <DescriptionListDescription>{formatBytes(systemInfo.availableDiskSpace)}</DescriptionListDescription>
                        </DescriptionListGroup>
                        <DescriptionListGroup>
                          <DescriptionListTerm>Disk Total</DescriptionListTerm>
                          <DescriptionListDescription>{formatBytes(systemInfo.totalDiskSpace)}</DescriptionListDescription>
                        </DescriptionListGroup>
                        <DescriptionListGroup>
                          <DescriptionListTerm>Warning threshold</DescriptionListTerm>
                          <DescriptionListDescription>30 GB</DescriptionListDescription>
                        </DescriptionListGroup>
                      </DescriptionList>
                    }
                  >
                    <button type="button" aria-label="Environment details" className="pf-v6-u-ml-xs" style={{ background: 'none', border: 'none', cursor: 'pointer', padding: 0, verticalAlign: 'middle' }}>
                      <InfoCircleIcon />
                    </button>
                  </Popover>
                </DescriptionListTerm>
                <DescriptionListDescription>
                  <Label status={getStatusLabelStatus(systemStatus.systemHealth)}>
                    {getStatusText(systemStatus.systemHealth)}
                  </Label>
                </DescriptionListDescription>
              </DescriptionListGroup>
              <DescriptionListGroup>
                <DescriptionListTerm>
                  <KeyIcon className="pf-v6-u-mr-sm" />
                  Pull Secret
                </DescriptionListTerm>
                <DescriptionListDescription>
                  <Label status={systemStatus.pullSecretDetected ? 'success' : 'warning'}>
                    {systemStatus.pullSecretDetected ? 'Present' : 'Missing'}
                  </Label>
                </DescriptionListDescription>
              </DescriptionListGroup>
            </DescriptionList>
          </CardBody>
        </Card>
      </PageSection>

      {/* Operation Statistics */}
      <PageSection>
        <Card>
          <CardHeader>
            <CardTitle>
              <Title headingLevel="h2">
                <ListIcon className="pf-v6-u-mr-sm" />
                Operation Statistics
              </Title>
            </CardTitle>
          </CardHeader>
          <CardBody>
            <Grid hasGutter>
              <GridItem md={3} sm={6}>
                <Card>
                  <CardBody style={{ textAlign: 'center' }}>
                    <Title headingLevel="h3" size="4xl">
                      {stats.totalOperations}
                    </Title>
                    <Label status="info">Total Operations</Label>
                  </CardBody>
                </Card>
              </GridItem>
              <GridItem md={3} sm={6}>
                <Card>
                  <CardBody style={{ textAlign: 'center' }}>
                    <Title headingLevel="h3" size="4xl">
                      {stats.successfulOperations}
                    </Title>
                    <Label status="success">
                      Successful
                    </Label>
                  </CardBody>
                </Card>
              </GridItem>
              <GridItem md={3} sm={6}>
                <Card>
                  <CardBody style={{ textAlign: 'center' }}>
                    <Title headingLevel="h3" size="4xl">
                      {stats.failedOperations}
                    </Title>
                    <Label status="danger">
                      Failed
                    </Label>
                  </CardBody>
                </Card>
              </GridItem>
              <GridItem md={3} sm={6}>
                <Card>
                  <CardBody style={{ textAlign: 'center' }}>
                    <Title headingLevel="h3" size="4xl">
                      {stats.runningOperations}
                    </Title>
                    <Label status="custom" icon={<SyncAltIcon />}>
                      Running
                    </Label>
                  </CardBody>
                </Card>
              </GridItem>
            </Grid>
          </CardBody>
        </Card>
      </PageSection>

      {/* Recent Operations */}
      <PageSection>
        <Card>
          <CardHeader>
            <CardTitle>
              <Title headingLevel="h2">
                <HistoryIcon className="pf-v6-u-mr-sm" />
                Recent Operations
              </Title>
            </CardTitle>
          </CardHeader>
          <CardBody className="pf-v6-u-p-0">
            {recentOperations.length === 0 ? (
              <EmptyState>
                <EmptyStateBody>No recent operations found.</EmptyStateBody>
              </EmptyState>
            ) : (
              <Table aria-label="Recent operations" variant="compact" borders={false}>
                <Thead>
                  <Tr>
                    <Th>Operation</Th>
                    <Th>Config</Th>
                    <Th>Status</Th>
                    <Th>Started</Th>
                    <Th>Duration</Th>
                  </Tr>
                </Thead>
                <Tbody>
                  {recentOperations.map((op, index) => (
                    <Tr key={index}>
                      <Td dataLabel="Operation">
                        {op.name}
                      </Td>
                      <Td dataLabel="Config">
                        {op.configFile}
                      </Td>
                      <Td dataLabel="Status">
                        {op.status === 'running' ? (
                          <Label status="custom" icon={<SyncAltIcon style={{ color: 'var(--pf-t--global--icon--color--inverse)' }} />}>Running</Label>
                        ) : (
                          <Label status={getOperationLabelStatus(op.status)}>
                            {getOperationStatusText(op.status)}
                          </Label>
                        )}
                      </Td>
                      <Td dataLabel="Started">
                        <Timestamp date={new Date(op.startedAt)} tooltip={{ variant: 'default' }} />
                      </Td>
                      <Td dataLabel="Duration">
                        <OutlinedClockIcon /> {op.duration ? formatDuration(op.duration) : '-'}
                      </Td>
                    </Tr>
                  ))}
                </Tbody>
              </Table>
            )}
          </CardBody>
        </Card>
      </PageSection>

    </>
  );
};

export default Dashboard;
