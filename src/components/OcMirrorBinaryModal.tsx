import { useEffect, useState } from 'react';
import axios from 'axios';
import {
  Alert,
  Button,
  Content,
  Label,
  Modal,
  ModalBody,
  ModalFooter,
  ModalHeader,
  ModalVariant,
  Radio,
  Spinner,
  Stack,
  StackItem,
} from '@patternfly/react-core';
import { useAlerts } from '../AlertContext';

type Binary = 'built-in' | 'redhat';

export interface OcMirrorStatus {
  selected: Binary;
  source: string;
  version: string;
  builtInSource: string;
  redhatDownloaded: boolean;
  switching: boolean;
}

// Where the active oc-mirror binary came from (ocMirrorSource in /api/system/info).
export const OC_MIRROR_SOURCE_LABELS: Record<string, string> = {
  source: 'Built from source',
  redhat: 'Red Hat build',
  'redhat-runtime': 'Red Hat build (downloaded)',
};

// The built-in binary is compiled from source unless the image was built with OC_MIRROR_FROM=redhat.
const builtInOption = (builtInSource: string) => {
  switch (builtInSource) {
    case 'source':
      return {
        label: 'Build from source',
        description: 'Compiled into this image from github.com/openshift/oc-mirror with updated Go dependencies. Works without internet access.',
      };
    case 'redhat':
      return {
        label: 'Built-in Red Hat build',
        description: 'Red Hat\'s oc-mirror as packaged into this image. Works without internet access.',
      };
    default:
      return { label: 'Built-in oc-mirror', description: 'The oc-mirror installed with this application.' };
  }
};

interface OcMirrorBinaryModalProps {
  isOpen: boolean;
  onClose: () => void;
  /** called after the binary was changed */
  onChanged: (status: OcMirrorStatus) => void;
}

export function OcMirrorBinaryModal({ isOpen, onClose, onChanged }: OcMirrorBinaryModalProps) {
  const { addSuccessAlert } = useAlerts();
  const [status, setStatus] = useState<OcMirrorStatus | null>(null);
  const [choice, setChoice] = useState<Binary>('built-in');
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');

  useEffect(() => {
    if (!isOpen) return;
    setStatus(null);
    setError('');
    axios.get<OcMirrorStatus>('/api/system/oc-mirror')
      .then(res => {
        setStatus(res.data);
        setChoice(res.data.selected);
      })
      .catch(() => setError('Could not load the oc-mirror settings.'));
  }, [isOpen]);

  const builtIn = builtInOption(status?.builtInSource ?? '');
  const downloading = saving && choice === 'redhat' && !status?.redhatDownloaded;

  const save = async () => {
    setSaving(true);
    setError('');
    try {
      const res = await axios.put<OcMirrorStatus>('/api/system/oc-mirror', { binary: choice });
      const label = choice === 'redhat' ? 'Official Red Hat Download' : builtIn.label;
      addSuccessAlert(`Now using ${label} (oc-mirror ${res.data.version}) for new operations`);
      onChanged(res.data);
      onClose();
    } catch (err: unknown) {
      const e = err as { response?: { data?: { error?: string } }; message?: string };
      setError(e.response?.data?.error || e.message || 'Could not change the oc-mirror binary.');
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal variant={ModalVariant.small} isOpen={isOpen} onClose={onClose} aria-labelledby="oc-mirror-binary-title">
      <ModalHeader
        title="oc-mirror binary"
        labelId="oc-mirror-binary-title"
        description="Choose which oc-mirror new mirror operations run."
      />
      <ModalBody>
        {!status && !error && <Spinner size="lg" aria-label="Loading oc-mirror settings" />}
        {status && (
          <Stack hasGutter>
            <StackItem>
              <Content component="p">
                Current: <strong>oc-mirror {status.version}</strong>
                {OC_MIRROR_SOURCE_LABELS[status.source] && (
                  <Label isCompact color="grey" className="pf-v6-u-ml-sm">{OC_MIRROR_SOURCE_LABELS[status.source]}</Label>
                )}
              </Content>
            </StackItem>
            {status.switching && !saving && (
              <StackItem>
                <Alert variant="info" isInline title="The oc-mirror binary is being changed in another session." />
              </StackItem>
            )}
            <StackItem>
              <Radio
                id="oc-mirror-binary-built-in"
                name="oc-mirror-binary"
                label={builtIn.label}
                description={builtIn.description}
                isChecked={choice === 'built-in'}
                isDisabled={saving}
                onChange={() => setChoice('built-in')}
              />
            </StackItem>
            <StackItem>
              <Radio
                id="oc-mirror-binary-redhat"
                name="oc-mirror-binary"
                label="Official Red Hat Download"
                description="Red Hat's latest published oc-mirror from mirror.openshift.com, verified against its SHA-256 checksum and kept in the data directory."
                body={status.redhatDownloaded
                  ? <Label isCompact color="green">Downloaded</Label>
                  : <Label isCompact color="blue">Downloaded when selected (needs internet access)</Label>}
                isChecked={choice === 'redhat'}
                isDisabled={saving}
                onChange={() => setChoice('redhat')}
              />
            </StackItem>
            <StackItem>
              <Content component="small">Operations that are already running keep the binary they started with.</Content>
            </StackItem>
          </Stack>
        )}
        {error && <Alert variant="danger" isInline title={error} className="pf-v6-u-mt-md" />}
      </ModalBody>
      <ModalFooter>
        <Button
          variant="primary"
          onClick={save}
          isLoading={saving}
          isDisabled={!status || saving || status.switching || choice === status.selected}
        >
          {downloading ? 'Downloading…' : saving ? 'Saving…' : 'Save'}
        </Button>
        <Button variant="link" onClick={onClose}>
          {saving ? 'Close' : 'Cancel'}
        </Button>
      </ModalFooter>
    </Modal>
  );
}
