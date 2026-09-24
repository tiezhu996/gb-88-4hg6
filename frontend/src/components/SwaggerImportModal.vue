<template>
  <a-modal
    v-model:visible="modalVisible"
    title="导入 OpenAPI 文档"
    :width="900"
    :mask-closable="false"
  >
    <template #footer>
      <a-space>
        <a-button :disabled="committing" @click="handleCancel">{{ hasPreview ? '关闭' : '取消' }}</a-button>
        <a-button
          v-if="hasPreview"
          :loading="parsing"
          :disabled="committing"
          @click="backToInput"
        >
          上一步
        </a-button>
        <a-button
          type="primary"
          :loading="parsing"
          :disabled="hasPreview || !documentText.trim()"
          @click="handleParse"
        >
          解析预览
        </a-button>
        <a-button
          v-if="hasPreview"
          type="primary"
          :loading="committing"
          :disabled="!!preview && preview.invalidCount > 0"
          @click="handleConfirm"
        >
          {{ hasRetriableFailures ? '重试失败条目' : '确认导入' }}
        </a-button>
      </a-space>
    </template>

    <!-- Step 1: paste or upload the document -->
    <div v-if="!hasPreview">
      <a-space style="margin-bottom: 12px">
        <a-upload
          :show-file-list="false"
          :auto-upload="false"
          accept=".json,application/json"
          @change="handleFileChange"
        >
          <a-button>
            <template #icon><icon-upload /></template>
            选择 JSON 文件
          </a-button>
        </a-upload>
        <span class="hint">支持 OpenAPI 2.0 / 3.0 JSON</span>
      </a-space>
      <a-textarea
        v-model="documentText"
        placeholder='粘贴 OpenAPI JSON，例如 { "openapi": "3.0.0", "paths": { ... } }'
        :auto-size="{ minRows: 12, maxRows: 18 }"
        style="font-family: monospace"
      />
    </div>

    <!-- Step 2: preview -->
    <div v-else-if="preview">
      <a-space wrap style="margin-bottom: 12px">
        <a-tag color="green">新增 {{ preview.newCount }}</a-tag>
        <a-tag color="orange">重复 {{ preview.dupCount }}</a-tag>
        <a-tag color="red">无法解析 {{ preview.invalidCount }}</a-tag>
      </a-space>

      <a-alert
        v-if="preview.invalidCount > 0"
        type="error"
        style="margin-bottom: 12px"
        :message="`存在 ${preview.invalidCount} 条无法解析的条目，请修正文档后重新解析，当前无法提交导入。`"
      />

      <a-table
        :data="entryRows"
        :pagination="false"
        :scroll="{ y: 300 }"
        size="small"
        row-key="rowKey"
        :expandable="{ width: 40 }"
      >
        <template #columns>
          <a-table-column title="方法" data-index="method" :width="90">
            <template #cell="{ record }">
              <a-tag :color="getMethodColor(record.method)">{{ record.method }}</a-tag>
            </template>
          </a-table-column>
          <a-table-column title="路径" data-index="path">
            <template #cell="{ record }">
              <code>{{ record.path }}</code>
              <a-tag v-if="record.status === 'duplicate'" color="orange" size="small" style="margin-left: 6px">
                重复
              </a-tag>
            </template>
          </a-table-column>
          <a-table-column title="状态码" data-index="statusCode" :width="80" />
          <a-table-column title="操作" :width="130">
            <template #cell="{ record }">
              <a-tag v-if="record.succeeded" color="green">已完成</a-tag>
              <a-select
                v-else
                :model-value="actions[actionKey(record)]"
                size="small"
                style="width: 110px"
                @update:model-value="(v) => setAction(record, v)"
              >
                <a-option
                  v-if="record.status === 'new'"
                  value="create"
                >新增</a-option>
                <template v-else>
                  <a-option value="skip">跳过</a-option>
                  <a-option value="replace">替换</a-option>
                </template>
              </a-select>
            </template>
          </a-table-column>
        </template>
        <template #expand-row="{ record }">
          <div class="expand-body">
            <div class="expand-label">响应体预览</div>
            <pre>{{ prettyBody(record.responseBody) }}</pre>
          </div>
        </template>
      </a-table>

      <template v-if="preview.invalid.length > 0">
        <div class="invalid-title">无法解析的条目（{{ preview.invalid.length }}）</div>
        <a-table
          :data="invalidRows"
          :pagination="false"
          :scroll="{ y: 160 }"
          size="small"
          row-key="rowKey"
        >
          <template #columns>
            <a-table-column title="方法" data-index="method" :width="110">
              <template #cell="{ record }">
                <a-tag v-if="record.method" color="red">{{ record.method }}</a-tag>
                <span v-else>-</span>
              </template>
            </a-table-column>
            <a-table-column title="路径" data-index="path">
              <template #cell="{ record }"><code>{{ record.path }}</code></template>
            </a-table-column>
            <a-table-column title="原因" data-index="reason" />
          </template>
        </a-table>
      </template>

      <!-- Per-entry failures from the last commit; preview is kept so the user can retry. -->
      <template v-if="commitResult && commitResult.failed > 0">
        <a-alert
          type="warning"
          style="margin: 12px 0"
          :message="`已新增 ${commitResult.created} 条、替换 ${commitResult.replaced} 条，另有 ${commitResult.failed} 条保存失败，可调整后重试。`"
        />
        <a-table
          :data="failureRows"
          :pagination="false"
          size="small"
          row-key="rowKey"
        >
          <template #columns>
            <a-table-column title="方法" data-index="method" :width="90">
              <template #cell="{ record }">
                <a-tag color="red">{{ record.method }}</a-tag>
              </template>
            </a-table-column>
            <a-table-column title="路径" data-index="path">
              <template #cell="{ record }"><code>{{ record.path }}</code></template>
            </a-table-column>
            <a-table-column title="失败原因" data-index="reason" />
          </template>
        </a-table>
      </template>
    </div>
  </a-modal>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue';
import { Message } from '@arco-design/web-vue';
import { IconUpload } from '@arco-design/web-vue/es/icon';
import { swaggerApi } from '../api';
import type {
  SwaggerPreviewEntry,
  SwaggerPreviewResult,
  SwaggerAction,
  SwaggerCommitResult,
  SwaggerImportFailure,
  SwaggerInvalidEntry
} from '../types';

const props = defineProps<{
  visible: boolean;
  projectId: string;
}>();

const emit = defineEmits<{
  (e: 'update:visible', value: boolean): void;
  (e: 'imported'): void;
}>();

const documentText = ref('');
const parsedDocument = ref<unknown>(null);
const preview = ref<SwaggerPreviewResult | null>(null);
const actions = ref<Record<string, SwaggerAction>>({});
const commitResult = ref<SwaggerCommitResult | null>(null);
// Entries already saved during a partial-success commit; excluded from retries.
const succeededKeys = ref<Set<string>>(new Set());
const parsing = ref(false);
const committing = ref(false);

const hasPreview = computed(() => preview.value !== null);

const modalVisible = computed({
  get: () => props.visible,
  set: (value: boolean) => emit('update:visible', value)
});

interface EntryRow extends SwaggerPreviewEntry {
  rowKey: string;
  succeeded: boolean;
}

const entryRows = computed<EntryRow[]>(() =>
  (preview.value?.entries ?? []).map((entry) => ({
    ...entry,
    rowKey: `entry-${entry.method}-${entry.path}`,
    succeeded: succeededKeys.value.has(actionKey(entry))
  }))
);

const hasRetriableFailures = computed(() => (commitResult.value?.failed ?? 0) > 0);

const invalidRows = computed(() =>
  (preview.value?.invalid ?? []).map((item: SwaggerInvalidEntry, index: number) => ({
    ...item,
    rowKey: `invalid-${index}-${item.method ?? ''}-${item.path}`
  }))
);

const failureRows = computed(() =>
  (commitResult.value?.failures ?? []).map((item: SwaggerImportFailure, index: number) => ({
    ...item,
    rowKey: `fail-${index}-${item.method}-${item.path}`
  }))
);

watch(
  () => props.visible,
  (visible) => {
    if (visible) {
      resetAll();
    }
  }
);

function resetAll() {
  documentText.value = '';
  parsedDocument.value = null;
  preview.value = null;
  actions.value = {};
  commitResult.value = null;
  succeededKeys.value = new Set();
  parsing.value = false;
  committing.value = false;
}

function actionKey(entry: SwaggerPreviewEntry) {
  return `${entry.method} ${entry.path}`;
}

function setAction(entry: SwaggerPreviewEntry, value: SwaggerAction) {
  actions.value = { ...actions.value, [actionKey(entry)]: value };
}

async function handleParse() {
  let doc: unknown;
  try {
    doc = JSON.parse(documentText.value);
  } catch {
    Message.error('JSON 格式错误，请检查文档内容');
    return;
  }
  parsing.value = true;
  try {
    const response = await swaggerApi.preview(props.projectId, doc);
    if (response.data.success && response.data.data) {
      preview.value = response.data.data;
      parsedDocument.value = doc;
      commitResult.value = null;
      succeededKeys.value = new Set();
      const initial: Record<string, SwaggerAction> = {};
      for (const entry of preview.value.entries) {
        // New entries are imported by default; duplicates are skipped by
        // default — the user must opt in to replacing them.
        initial[actionKey(entry)] = entry.status === 'duplicate' ? 'skip' : 'create';
      }
      actions.value = initial;
      if (preview.value.entries.length === 0 && preview.value.invalidCount === 0) {
        Message.warning('文档中未解析出任何接口');
      }
    }
  } catch (error: unknown) {
    Message.error(errorMessage(error));
  } finally {
    parsing.value = false;
  }
}

async function handleConfirm() {
  if (!preview.value) {
    return;
  }
  if (preview.value.invalidCount > 0) {
    Message.warning('存在无法解析的条目，请先修正文档');
    return;
  }
  const selections = preview.value.entries
    .filter((entry) => !succeededKeys.value.has(actionKey(entry)))
    .map((entry) => ({
      method: entry.method,
      path: entry.path,
      action: actions.value[actionKey(entry)] ?? (entry.status === 'duplicate' ? 'skip' : 'create')
    }))
    .filter((s) => s.action !== 'skip');

  if (selections.length === 0) {
    Message.warning('没有需要重试的失败条目');
    return;
  }

  committing.value = true;
  try {
    const response = await swaggerApi.commit(props.projectId, parsedDocument.value, selections);
    if (response.data.success && response.data.data) {
      const result = response.data.data;
      commitResult.value = result;
      // Everything not reported as failed has been saved; mark it so retries
      // only resend the failed entries.
      const nextSucceeded = new Set(succeededKeys.value);
      for (const sel of selections) {
        const failed = result.failures.some(
          (f) => f.method === sel.method && f.path === sel.path && f.action === sel.action
        );
        if (!failed) {
          nextSucceeded.add(`${sel.method} ${sel.path}`);
        }
      }
      succeededKeys.value = nextSucceeded;

      if (result.failed === 0) {
        Message.success(`导入完成：新增 ${result.created} 条，替换 ${result.replaced} 条`);
        emit('imported');
        emit('update:visible', false);
      } else {
        Message.warning(`部分条目保存失败（${result.failed} 条），可调整后重试`);
        emit('imported');
      }
    }
  } catch (error: unknown) {
    Message.error(errorMessage(error));
  } finally {
    committing.value = false;
  }
}

function handleFileChange(fileItem: { file?: File }) {
  const file = fileItem.file
  if (!file) {
    return
  }
  const reader = new FileReader()
  reader.onload = () => {
    documentText.value = String(reader.result ?? '')
  }
  reader.onerror = () => Message.error('读取文件失败')
  reader.readAsText(file)
}

function backToInput() {
  preview.value = null;
  commitResult.value = null;
  succeededKeys.value = new Set();
}

function handleCancel() {
  emit('update:visible', false);
}

function getMethodColor(method: string) {
  const colors: Record<string, string> = {
    GET: 'green',
    POST: 'blue',
    PUT: 'orange',
    DELETE: 'red',
    PATCH: 'purple'
  };
  return colors[method] || 'gray';
}

function prettyBody(body: string) {
  try {
    return JSON.stringify(JSON.parse(body), null, 2);
  } catch {
    return body;
  }
}

function errorMessage(error: unknown): string {
  const err = error as { response?: { data?: { error?: string; message?: string } } };
  return err.response?.data?.error || err.response?.data?.message || '请求失败，请稍后重试';
}
</script>

<style scoped>
.hint {
  color: #86909c;
  font-size: 12px;
}

.invalid-title {
  margin: 16px 0 8px;
  font-weight: 600;
  color: #cb2634;
}

.expand-body {
  padding: 4px 8px;
}

.expand-label {
  font-size: 12px;
  color: #4e5969;
  margin-bottom: 4px;
}

.expand-body pre {
  margin: 0;
  padding: 8px;
  background: #f7f8fa;
  border-radius: 4px;
  font-size: 12px;
  max-height: 180px;
  overflow: auto;
}
</style>
