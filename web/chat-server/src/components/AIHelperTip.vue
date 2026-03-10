<template>
  <div v-if="aiEnabled" class="ai-helper-tip">
    <div class="ai-tip-content">
      <el-icon class="ai-icon"><ChatDotRound /></el-icon>
      <span class="ai-tip-text">
        💬 提示：输入 <strong>@ai</strong> 开头的消息可以与AI助手对话
      </span>
      <el-button 
        v-if="showClearButton" 
        size="small" 
        type="info" 
        text
        @click="handleClearContext"
        class="clear-btn"
      >
        清除上下文
      </el-button>
    </div>
  </div>
</template>

<script>
import { ref, onMounted } from 'vue';
import { ChatDotRound } from '@element-plus/icons-vue';
import { getAIStatus, clearAIContext } from '@/utils/aiHelper';
import { ElMessage } from 'element-plus';
import axios from 'axios';
import { useStore } from 'vuex';

export default {
  name: 'AIHelperTip',
  components: {
    ChatDotRound
  },
  props: {
    showClearButton: {
      type: Boolean,
      default: false
    },
    userId: {
      type: String,
      default: ''
    }
  },
  setup(props) {
    const store = useStore();
    const aiEnabled = ref(false);

    onMounted(async () => {
      const status = await getAIStatus(axios, store.state.backendUrl);
      aiEnabled.value = status.enabled;
    });

    const handleClearContext = async () => {
      if (!props.userId) {
        ElMessage.warning('用户信息未加载');
        return;
      }
      const success = await clearAIContext(axios, store.state.backendUrl, props.userId);
      if (success) {
        ElMessage.success('AI对话上下文已清除');
      } else {
        ElMessage.error('清除失败，请稍后重试');
      }
    };

    return {
      aiEnabled,
      handleClearContext
    };
  }
};
</script>

<style scoped>
.ai-helper-tip {
  background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
  padding: 8px 15px;
  border-radius: 8px;
  margin-bottom: 10px;
  animation: slideDown 0.3s ease-out;
}

@keyframes slideDown {
  from {
    opacity: 0;
    transform: translateY(-10px);
  }
  to {
    opacity: 1;
    transform: translateY(0);
  }
}

.ai-tip-content {
  display: flex;
  align-items: center;
  color: white;
  font-size: 13px;
}

.ai-icon {
  font-size: 18px;
  margin-right: 8px;
  animation: pulse 2s ease-in-out infinite;
}

@keyframes pulse {
  0%, 100% {
    transform: scale(1);
  }
  50% {
    transform: scale(1.1);
  }
}

.ai-tip-text {
  flex: 1;
}

.ai-tip-text strong {
  font-weight: bold;
  background: rgba(255, 255, 255, 0.3);
  padding: 2px 6px;
  border-radius: 4px;
  margin: 0 4px;
}

.clear-btn {
  color: white !important;
  border-color: rgba(255, 255, 255, 0.5) !important;
}

.clear-btn:hover {
  background: rgba(255, 255, 255, 0.2) !important;
}
</style>
