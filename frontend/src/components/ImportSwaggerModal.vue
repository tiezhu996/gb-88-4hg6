<template>
  <a-modal
    :visible="visible"
    title="导入 OpenAPI 文档"
    :width="860"
    :mask-closable="false"
    :ok-text="commitButtonText"
    :ok-loading="committing"
    :cancel-text="hasPreview ? '关闭' : '取消'"
    :ok-button-props="{ disabled: !canCommit }"
    @ok="handleConfirm"
    @cancel="handleCancel"
    @close="handleCancel"
  >
    <div class="import-modal">
      <!-- Step 1: choose document -->
      <div class="upload-section">
        <a-upload
          :custom-request="handleUpload"
          :show-file-list="false"
          accept=".json,application/json"
          :disabled="parsing"
        >
          <a-button type="primary" :loading="parsing">
            <template #icon><icon-upload /></template>
            选择 JSON 文件
          </a-button>
        </a-upload>
        <span class="upload-hint">支持 OpenAPI/Swagger 2.0、3.0 的 JSON 文档</span>
      </div>
      <a-divider class="inline-divider" orientation="center">或粘贴文档内容</a-divider>
      <MonacoEditor v-model="documentText" language="json" style="height: 220px" />
      <div class="parse-row">
        <a-button type="outline" :disabled="!documentText.trim() || parsing" :loading="parsing" @click="handlePreview">
          解析并预览
        </a-button>
        <span v-if="fileName" class="file-name">当前文件：{{ fileName }}</span>
      </div>

      <!-- Step 2: preview -->
      <template v-if="preview">
        <a-divider />
        <a-space class="summary-tags" wrap>
          <a-tag color="green">新增 {{ preview.new.length }}</a-tag>
          <a-tag color="orange">重复 {{ preview.duplicate.length }}</a-tag>
          <a-tag color="red">无法解析 {{ preview.invalid.length }}</a-tag>
          <a-tag v-if="selectedCount > 0" color="arcoblue">本次将写入 {{ selectedCount }} 条</a-tag>
        </a-space>

        <a-alert v-if="preview.invalid.length > 0" type="error" class="preview-alert">
          存在 {{ preview.invalid.length }} 条无法解析的条目，请修正文档后重新预览，当前无法提交导入。
        </a-alert>
        <a-alert v-else type="info" class="preview-alert">
          请确认重复条目的处理方式（跳过或替换）。替换会更新原有接口的状态码与响应体，原有请求日志仍关联该接口。
        </a-alert>

        <!-- Failed entries from the last commit, kept for retry -->
        <a-alert v-if="commitFailures.length > 0" type="warning" class="preview-alert">
          上次提交有 {{ commitFailures.length }} 条保存失败，已保留在预览中，调整后可重试。
        </a-alert>

        <a-tabs default-active-key="new">
          <a-tab-pane key="new" :title="`新增 (${preview.new.length})`">
            <a-table :data="preview.new" :pagination="false" size="small" :scroll="{ maxHeight: 240 }">
              <template #columns>
                <a-table-column title="方法" data-index="method" :width="90">
                  <template #cell="{ record }">
                    <a-tag :color="getMethodColor(record.method)" size="small">{{ record.method }}</a-tag>
                  </template>
                </a-table-column>
                <a-table-column title="路径" data-index="path">
                  <template #cell="{ record }"><code>{{ record.path }}</code></template>
                </a-table-column>
                <a-table-column title="状态码" data-index="statusCode" :width="80" />
                <a-table-column title="导入" :width="80" align="right">
                  <template #cell="{ record }">
                    <a-checkbox
                      :model-value="newActions[entryKey(record)] !== false"
                      @change="(v: boolean | (string | number | boolean)[]) => toggleNew(record, Boolean(v))"
                    >
                      导入
                    </a-checkbox>
                  </template>
                </a-table-column>
              </template>
              <template #empty><a-empty description="无新增条目" /></template>
            </a-table>
          </a-tab-pane>

          <a-tab-pane key="duplicate" :title="`重复 (${preview.duplicate.length})`">
            <a-table :data="preview.duplicate" :pagination="false" size="small" :scroll="{ maxHeight: 240 }">
              <template #columns>
                <a-table-column title="方法" data-index="method" :width="90">
                  <template #cell="{ record }">
                    <a-tag :color="getMethodColor(record.method)" size="small">{{ record.method }}</a-tag>
                  </template>
                </a-table-column>
                <a-table-column title="路径" data-index="path">
                  <template #cell="{ record }"><code>{{ record.path }}</code></template>
                </a-table-column>
                <a-table-column title="处理方式" :width="200">
                  <template #cell="{ record }">
                    <a-radio-group
                      :model-value="duplicateActions[entryKey(record)] || 'skip'"
                      type="button"
                      size="small"
                      @change="(v: string | number | boolean) => setDuplicateAction(record, String(v))"
                    >
                      <a-radio value="skip">跳过</a-radio>
                      <a-radio value="replace">替换</a-radio>
                    </a-radio-group>
                  </template>
                </a-table-column>
              </template>
              <template #empty><a-empty description="无重复条目" /></template>
            </a-table>
          </a-tab-pane>

          <a-tab-pane key="invalid" :title="`无法解析 (${preview.invalid.length})`">
            <a-table :data="preview.invalid" :pagination="false" size="small" :scroll="{ maxHeight: 240 }">
              <template #columns>
                <a-table-column title="方法" data-index="method" :width="90">
                  <template #cell="{ record }">
                    <a-tag color="red" size="small">{{ record.method || '—' }}</a-tag>
                  </template>
                </a-table-column>
                <a-table-column title="路径" data-index="path">
                  <template #cell="{ record }"><code>{{ record.path || '—' }}</code></template>
                </a-table-column>
                <a-table-column title="原因" data-index="reason" />
              </template>
              <template #empty><a-empty description="文档全部可解析" /></template>
            </a-table>
          </a-tab-pane>
        </a-tabs>
      </template>
    </div>
  </a-modal>
</template>

<script setup lang="ts">
import { ref, computed, watch } from 'vue';
import { Message } from '@arco-design/web-vue';
import { IconUpload } from '@arco-design/web-vue/es/icon';
import type { RequestOption } from '@arco-design/web-vue/es/upload/interfaces';
import MonacoEditor from './MonacoEditor.vue';
import { swaggerApi } from '../api';
import type {
  ImportPreview,
  ImportPreviewItem,
  ImportAction,
  ImportSelection,
  ImportFailure
} from '../types';

const props = defineProps<{ visible: boolean; projectId: string }>();
const emit = defineEmits<{
  (e: 'update:visible', value: boolean): void;
  (e: 'imported'): void;
}>();

const documentText = ref('');
const fileName = ref('');
const parsing = ref(false);
const committing = ref(false);
const preview = ref<ImportPreview | null>(null);
const parsedDocument = ref<unknown>(null);
// New entries default to create; set to false when the user unchecks one.
const newActions = ref<Record<string, boolean>>({});
const duplicateActions = ref<Record<string, ImportAction>>({});
const commitFailures = ref<ImportFailure[]>([]);

const hasPreview = computed(() => preview.value !== null);
const hasInvalid = computed(() => (preview.value?.invalid.length ?? 0) > 0);

function entryKey(item: { method: string; path: string }) {
  return `${item.method} ${item.path}`;
}

function failureKeySet() {
  return new Set(commitFailures.value.map((f) => entryKey(f)));
}

const selectedNewKeys = computed(
  () =>
    new Set(
      preview.value
        ? preview.value.new
            .filter((item) => newActions.value[entryKey(item)] !== false)
            .map((item) => entryKey(item))
        : []
    )
);

const selectedReplaceKeys = computed(
  () =>
    new Set(
      preview.value
        ? preview.value.duplicate
            .filter((item) => (duplicateActions.value[entryKey(item)] || 'skip') === 'replace')
            .map((item) => entryKey(item))
        : []
    )
);

const selectedCount = computed(() => selectedNewKeys.value.size + selectedReplaceKeys.value.size);

const canCommit = computed(
  () =>
    hasPreview.value &&
    !hasInvalid.value &&
    selectedCount.value > 0 &&
    !parsing.value &&
    !committing.value
);

const commitButtonText = computed(() => {
  if (!hasPreview.value) return '提交';
  if (commitFailures.value.length > 0) return `重试失败条目（${selectedCount.value} 条）`;
  return `确认导入（${selectedCount.value} 条）`;
});

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

function readFile(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result ?? ''));
    reader.onerror = () => reject(reader.error);
    reader.readAsText(file);
  });
}

function handleUpload(option: RequestOption) {
  const rawFile = option.fileItem.file;
  if (!rawFile) {
    option.onError(new Error('未获取到文件'));
    return {};
  }
  readFile(rawFile)
    .then((text) => {
      JSON.parse(text); // fail fast on malformed JSON
      documentText.value = text;
      fileName.value = rawFile.name;
      option.onSuccess();
      Message.success('文件已加载，点击「解析并预览」');
    })
    .catch(() => {
      option.onError(new Error('invalid json'));
      Message.error('文件不是合法的 JSON，请检查后重试');
    });
  return {};
}

async function handlePreview() {
  let doc: unknown;
  try {
    doc = JSON.parse(documentText.value);
  } catch {
    Message.error('文档不是合法的 JSON，无法解析');
    return;
  }
  if (!doc || typeof doc !== 'object') {
    Message.error('文档必须是 JSON 对象');
    return;
  }
  parsing.value = true;
  try {
    const response = await swaggerApi.preview(props.projectId, doc);
    if (response.data.success && response.data.data) {
      preview.value = response.data.data;
      parsedDocument.value = doc;
      newActions.value = {};
      duplicateActions.value = {};
      commitFailures.value = [];
      if (response.data.data.invalid.length > 0) {
        Message.warning(`解析完成，其中 ${response.data.data.invalid.length} 条无法解析`);
      } else {
        Message.success('解析完成，请确认导入内容');
      }
    }
  } catch (error: any) {
    Message.error(error.response?.data?.error || error.message || '解析失败');
  } finally {
    parsing.value = false;
  }
}

function toggleNew(item: ImportPreviewItem, include: boolean) {
  newActions.value[entryKey(item)] = include;
}

function setDuplicateAction(item: ImportPreviewItem, action: string) {
  duplicateActions.value[entryKey(item)] = action as ImportAction;
}

function buildSelections(): ImportSelection[] {
  if (!preview.value) return [];
  const selections: ImportSelection[] = [];
  for (const item of preview.value.new) {
    if (newActions.value[entryKey(item)] !== false) {
      selections.push({ method: item.method, path: item.path, action: 'create' });
    }
  }
  for (const item of preview.value.duplicate) {
    // Skipped duplicates are simply not sent; only replacements are written.
    if ((duplicateActions.value[entryKey(item)] || 'skip') === 'replace') {
      selections.push({ method: item.method, path: item.path, action: 'replace' });
    }
  }
  return selections;
}

async function handleConfirm() {
  if (!preview.value || hasInvalid.value) {
    if (hasInvalid.value) Message.warning('存在无法解析的条目，请先修正文档');
    return;
  }
  // When retrying after partial failures, only send the surviving (failed)
  // entries rather than re-sending the ones already saved.
  const allSelections = buildSelections();
  const failedSet = failureKeySet();
  const selections =
    commitFailures.value.length > 0
      ? allSelections.filter((sel) => failedSet.has(entryKey(sel)))
      : allSelections;

  if (selections.length === 0) {
    Message.info('没有需要导入的条目');
    return;
  }

  committing.value = true;
  try {
    const response = await swaggerApi.commit(props.projectId, parsedDocument.value, selections);
    if (!response.data.success || !response.data.data) return;
    const result = response.data.data;

    // Keep only failed rows in the preview so the user can adjust and retry;
    // successfully created/replaced entries disappear from the tables.
    const failedSetAfter = new Set(result.failed.map((f) => entryKey(f)));
    if (preview.value) {
      preview.value = {
        new: preview.value.new.filter((item) => failedSetAfter.has(entryKey(item))),
        duplicate: preview.value.duplicate.filter((item) => failedSetAfter.has(entryKey(item))),
        invalid: preview.value.invalid
      };
    }
    commitFailures.value = result.failed;

    emit('imported');

    if (result.failed.length === 0) {
      Message.success(`导入完成：新增 ${result.created} 条，替换 ${result.replaced} 条`);
      close();
      return;
    }
    const detail = result.failed
      .slice(0, 5)
      .map((f) => `${f.method} ${f.path}：${f.reason}`)
      .join('；');
    Message.warning({
      content: `成功 ${result.created + result.replaced} 条，失败 ${result.failed.length} 条。${detail}`,
      duration: 6000,
      closable: true
    });
  } catch (error: any) {
    Message.error(error.response?.data?.error || error.message || '提交失败');
  } finally {
    committing.value = false;
  }
}

function handleCancel() {
  // Closing discards an in-progress parse but keeps the editor text for reuse.
  if (committing.value) return;
  close();
}

function close() {
  preview.value = null;
  parsedDocument.value = null;
  commitFailures.value = [];
  newActions.value = {};
  duplicateActions.value = {};
  emit('update:visible', false);
}

function reset() {
  documentText.value = '';
  fileName.value = '';
  preview.value = null;
  parsedDocument.value = null;
  commitFailures.value = [];
  newActions.value = {};
  duplicateActions.value = {};
}

// Start from a clean upload/editor state every time the modal opens.
watch(
  () => props.visible,
  (visible) => {
    if (visible) reset();
  }
);
</script>

<style scoped>
.import-modal {
  max-height: 70vh;
  overflow-y: auto;
  padding-right: 4px;
}

.upload-section {
  display: flex;
  align-items: center;
  gap: 12px;
}

.upload-hint {
  font-size: 12px;
  color: #86909c;
}

.inline-divider {
  margin: 12px 0;
}

.parse-row {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-top: 12px;
}

.file-name {
  font-size: 12px;
  color: #4e5969;
}

.summary-tags {
  margin-bottom: 12px;
}

.preview-alert {
  margin-bottom: 12px;
}
</style>
