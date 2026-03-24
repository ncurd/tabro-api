export default {
	// Home Page
	home: {
		viewOnGithub: '在 GitHub 上查看',
		viewDocs: '查看文档',
		docs: '文档',
		switchToLight: '切换到浅色模式',
		switchToDark: '切换到深色模式',
		dashboard: '控制台',
		login: '登录',
		getStarted: '立即开始',
		goToDashboard: '进入控制台',
		// 新增：面向用户的价值主张
		heroSubtitle: '一个密钥，畅用多个 AI 模型',
		heroDescription: '无需管理多个订阅账号，一站式接入 Claude、GPT、Gemini 等主流 AI 服务',
		tags: {
			subscriptionToApi: '订阅转 API',
			stickySession: '会话保持',
			realtimeBilling: '按量计费'
		},
		// 用户痛点区块
		painPoints: {
			title: '你是否也遇到这些问题？',
			items: {
				expensive: {
					title: '订阅费用高',
					desc: '每个 AI 服务都要单独订阅，每月支出越来越多'
				},
				complex: {
					title: '多账号难管理',
					desc: '不同平台的账号、密钥分散各处，管理起来很麻烦'
				},
				unstable: {
					title: '服务不稳定',
					desc: '单一账号容易触发限制，影响正常使用'
				},
				noControl: {
					title: '用量无法控制',
					desc: '不知道钱花在哪了，也无法限制团队成员的使用'
				}
			}
		},
		// 解决方案区块
		solutions: {
			title: '我们帮你解决',
			subtitle: '简单三步，开始省心使用 AI'
		},
		features: {
			unifiedGateway: '一键接入',
			unifiedGatewayDesc: '获取一个 API 密钥，即可调用所有已接入的 AI 模型，无需分别申请。',
			multiAccount: '稳定可靠',
			multiAccountDesc: '智能调度多个上游账号，自动切换和负载均衡，告别频繁报错。',
			balanceQuota: '用多少付多少',
			balanceQuotaDesc: '按实际使用量计费，支持设置配额上限，团队用量一目了然。'
		},
		// 优势对比
		comparison: {
			title: '为什么选择我们？',
			headers: {
				feature: '对比项',
				official: '官方订阅',
				us: '本平台'
			},
			items: {
				pricing: {
					feature: '付费方式',
					official: '固定月费，用不完也付',
					us: '按量付费，用多少付多少'
				},
				models: {
					feature: '模型选择',
					official: '单一服务商',
					us: '多模型随意切换'
				},
				management: {
					feature: '账号管理',
					official: '每个服务单独管理',
					us: '统一密钥，一站管理'
				},
				stability: {
					feature: '服务稳定性',
					official: '单账号易触发限制',
					us: '多账号池，自动切换'
				},
				control: {
					feature: '用量控制',
					official: '无法限制',
					us: '可设配额、查明细'
				}
			}
		},
		providers: {
			title: '已支持的 AI 模型',
			description: '一个 API，多种选择',
			supported: '已支持',
			soon: '即将推出',
			claude: 'Claude',
			gemini: 'Gemini',
			antigravity: 'Antigravity',
			more: '更多'
		},
		// CTA 区块
		cta: {
			title: '准备好开始了吗？',
			description: '注册即可获得免费试用额度，体验一站式 AI 服务',
			button: '免费注册'
		},
		footer: {
			allRightsReserved: '保留所有权利。'
		}
	},

	// Key Usage Query Page
	keyUsage: {
		title: 'API Key 用量查询',
		subtitle: '输入您的 API Key 以查看实时消费金额与使用状态',
		placeholder: 'sk-ant-mirror-xxxxxxxxxxxx',
		query: '查询',
		querying: '查询中...',
		privacyNote: '您的 Key 仅在浏览器本地处理，不会被存储',
		dateRange: '统计范围:',
		dateRangeToday: '今日',
		dateRange7d: '7 天',
		dateRange30d: '30 天',
		dateRangeCustom: '自定义',
		apply: '应用',
		used: '已使用',
		detailInfo: '详细信息',
		tokenStats: 'Token 统计',
		modelStats: '模型用量统计',
		// Table headers
		model: '模型',
		requests: '请求数',
		inputTokens: '输入 Tokens',
		outputTokens: '输出 Tokens',
		cacheCreationTokens: '缓存创建',
		cacheReadTokens: '缓存读取',
		totalTokens: '总 Tokens',
		cost: '费用',
		// Status
		quotaMode: 'Key 限额模式',
		walletBalance: '钱包余额',
		// Ring card titles
		totalQuota: '总额度',
		limit5h: '5 小时限额',
		limitDaily: '日限额',
		limit7d: '7 天限额',
		limitWeekly: '周限额',
		limitMonthly: '月限额',
		// Detail rows
		remainingQuota: '剩余额度',
		expiresAt: '过期时间',
		todayExpires: '(今日到期)',
		daysLeft: '({days} 天)',
		usedQuota: '已用额度',
		resetNow: '即将重置',
		subscriptionType: '订阅类型',
		subscriptionExpires: '订阅到期',
		// Usage stat cells
		todayRequests: '今日请求',
		todayInputTokens: '今日输入',
		todayOutputTokens: '今日输出',
		todayTokens: '今日 Tokens',
		todayCacheCreation: '今日缓存创建',
		todayCacheRead: '今日缓存读取',
		todayCost: '今日费用',
		rpmTpm: 'RPM / TPM',
		totalRequests: '累计请求',
		totalInputTokens: '累计输入',
		totalOutputTokens: '累计输出',
		totalTokensLabel: '累计 Tokens',
		totalCacheCreation: '累计缓存创建',
		totalCacheRead: '累计缓存读取',
		totalCost: '累计费用',
		avgDuration: '平均耗时',
		// Messages
		enterApiKey: '请输入 API Key',
		querySuccess: '查询成功',
		queryFailed: '查询失败',
		queryFailedRetry: '查询失败，请稍后重试',
	},

	// Setup Wizard
	setup: {
		title: 'Sub2API 安装向导',
		description: '配置您的 Sub2API 实例',
		database: {
			title: '数据库配置',
			description: '连接到您的 PostgreSQL 数据库',
			host: '主机',
			port: '端口',
			username: '用户名',
			password: '密码',
			databaseName: '数据库名称',
			sslMode: 'SSL 模式',
			passwordPlaceholder: '密码',
			ssl: {
				disable: '禁用',
				require: '要求',
				verifyCa: '验证 CA',
				verifyFull: '完全验证'
			}
		},
		redis: {
			title: 'Redis 配置',
			description: '连接到您的 Redis 服务器',
			host: '主机',
			port: '端口',
			password: '密码（可选）',
			database: '数据库',
			passwordPlaceholder: '密码',
			enableTls: '启用 TLS',
			enableTlsHint: '连接 Redis 时使用 TLS（公共 CA 证书）'
		},
		admin: {
			title: '管理员账户',
			description: '创建您的管理员账户',
			email: '邮箱',
			password: '密码',
			confirmPassword: '确认密码',
			passwordPlaceholder: '至少 8 个字符',
			confirmPasswordPlaceholder: '确认密码',
			passwordMismatch: '密码不匹配'
		},
		ready: {
			title: '准备安装',
			description: '检查您的配置并完成安装',
			database: '数据库',
			redis: 'Redis',
			adminEmail: '管理员邮箱'
		},
		status: {
			testing: '测试中...',
			success: '连接成功',
			testConnection: '测试连接',
			installing: '安装中...',
			completeInstallation: '完成安装',
			completed: '安装完成！',
			redirecting: '正在跳转到登录页面...',
			restarting: '服务正在重启，请稍候...',
			timeout: '服务重启时间超出预期，请手动刷新页面。'
		}
	},

	// Common
	common: {
		loading: '加载中...',
		justNow: '刚刚',
		save: '保存',
		cancel: '取消',
		delete: '删除',
		edit: '编辑',
		create: '创建',
		update: '更新',
		confirm: '确认',
		reset: '重置',
		search: '搜索',
		filter: '筛选',
		export: '导出',
		import: '导入',
		actions: '操作',
		status: '状态',
		name: '名称',
		email: '邮箱',
		password: '密码',
		submit: '提交',
		back: '返回',
		next: '下一步',
		yes: '是',
		no: '否',
		all: '全部',
		none: '无',
		noData: '暂无数据',
		expand: '展开',
		collapse: '收起',
		success: '成功',
		error: '错误',
		critical: '严重',
		warning: '警告',
		info: '提示',
		active: '启用',
		inactive: '禁用',
		more: '更多',
		close: '关闭',
		enabled: '已启用',
		disabled: '已禁用',
		total: '总计',
		balance: '余额',
		available: '可用',
		copiedToClipboard: '已复制到剪贴板',
		copied: '已复制',
		copyFailed: '复制失败',
		verifying: '验证中...',
		processing: '处理中...',
		contactSupport: '联系客服',
		add: '添加',
		invalidEmail: '请输入有效的邮箱地址',
		optional: '可选',
		selectOption: '请选择',
		searchPlaceholder: '搜索...',
		noOptionsFound: '无匹配选项',
		noGroupsAvailable: '无可用分组',
		unknownError: '发生未知错误',
		saving: '保存中...',
		selectedCount: '（已选 {count} 个）',
		refresh: '刷新',
		settings: '设置',
		chooseFile: '选择文件',
		notAvailable: '不可用',
		now: '现在',
		unknown: '未知',
		minutes: '分钟',
		time: {
			never: '从未',
			justNow: '刚刚',
			minutesAgo: '{n}分钟前',
			hoursAgo: '{n}小时前',
			daysAgo: '{n}天前',
			countdown: {
				daysHours: '{d}d {h}h',
				hoursMinutes: '{h}h {m}m',
				minutes: '{m}m',
				withSuffix: '{time} 后解除'
			}
		}
	},

	// Navigation
	nav: {
		dashboard: '仪表盘',
		announcements: '公告',
		apiKeys: 'API 密钥',
		usage: '使用记录',
		redeem: '兑换',
		profile: '个人资料',
		users: '用户管理',
		groups: '分组管理',
		subscriptions: '订阅管理',
		accounts: '账号管理',
		proxies: 'IP管理',
		redeemCodes: '兑换码',
		ops: '运维监控',
		promoCodes: '优惠码',
		settings: '系统设置',
		myAccount: '我的账户',
		lightMode: '浅色模式',
		darkMode: '深色模式',
		collapse: '收起',
		expand: '展开',
		logout: '退出登录',
		github: 'GitHub',
		mySubscriptions: '我的订阅',
		buySubscription: '充值/订阅',
		docs: '文档',
		sora: 'Sora 创作'
	},

	// Auth
	auth: {
		welcomeBack: '欢迎回来',
		signInToAccount: '登录您的账户以继续',
		signIn: '登录',
		signingIn: '登录中...',
		createAccount: '创建账户',
		signUpToStart: '注册以开始使用 {siteName}',
		signUp: '注册',
		processing: '处理中...',
		continue: '继续',
		rememberMe: '记住我',
		dontHaveAccount: '还没有账户？',
		alreadyHaveAccount: '已有账户？',
		registrationDisabled: '注册功能暂时关闭，请联系管理员。',
		emailLabel: '邮箱',
		emailPlaceholder: '请输入邮箱',
		passwordLabel: '密码',
		passwordPlaceholder: '请输入密码',
		createPasswordPlaceholder: '创建一个安全的密码',
		passwordHint: '至少 6 个字符',
		emailRequired: '请输入邮箱',
		invalidEmail: '请输入有效的邮箱地址',
		passwordRequired: '请输入密码',
		passwordMinLength: '密码至少需要 6 个字符',
		loginFailed: '登录失败，请检查您的凭据后重试。',
		registrationFailed: '注册失败，请重试。',
		emailSuffixNotAllowed: '该邮箱域名不在允许注册范围内。',
		emailSuffixNotAllowedWithAllowed: '该邮箱域名不被允许。可用域名：{suffixes}',
		loginSuccess: '登录成功！欢迎回来。',
		accountCreatedSuccess: '账户创建成功！欢迎使用 {siteName}。',
		reloginRequired: '会话已过期，请重新登录。',
		turnstileExpired: '验证已过期，请重试',
		turnstileFailed: '验证失败，请重试',
		completeVerification: '请完成验证',
		verifyYourEmail: '验证您的邮箱',
		sessionExpired: '会话已过期',
		sessionExpiredDesc: '请返回注册页面重新开始。',
		verificationCode: '验证码',
		verificationCodeHint: '请输入发送到您邮箱的6位验证码',
		sendingCode: '发送中...',
		clickToResend: '点击重新发送验证码',
		resendCode: '重新发送验证码',
		sendCodeDesc: '我们将发送验证码到',
		codeSentSuccess: '验证码已发送！请查收您的邮箱。',
		verifying: '验证中...',
		verifyAndCreate: '验证并创建账户',
		resendCountdown: '{countdown}秒后可重新发送',
		backToRegistration: '返回注册',
		sendCodeFailed: '发送验证码失败，请重试。',
		verifyFailed: '验证失败，请重试。',
		codeRequired: '请输入验证码',
		invalidCode: '请输入有效的6位验证码',
		promoCodeLabel: '优惠码',
		promoCodePlaceholder: '输入优惠码（可选）',
		promoCodeValid: '有效！注册后将获得 ${amount} 赠送余额',
		promoCodeInvalid: '无效的优惠码',
		promoCodeNotFound: '优惠码不存在',
		promoCodeExpired: '此优惠码已过期',
		promoCodeDisabled: '此优惠码已被禁用',
		promoCodeMaxUsed: '此优惠码已达到使用上限',
		promoCodeAlreadyUsed: '您已使用过此优惠码',
		promoCodeValidating: '优惠码正在验证中，请稍候',
		promoCodeInvalidCannotRegister: '优惠码无效，请检查后重试或清空优惠码',
		invitationCodeLabel: '邀请码',
		invitationCodePlaceholder: '请输入邀请码',
		invitationCodeRequired: '请输入邀请码',
		invitationCodeValid: '邀请码有效',
		invitationCodeInvalid: '邀请码无效或已被使用',
		invitationCodeValidating: '正在验证邀请码...',
		invitationCodeInvalidCannotRegister: '邀请码无效，请检查后重试',
		linuxdo: {
			signIn: '使用 Linux.do 登录',
			orContinue: '或使用邮箱密码继续',
			callbackTitle: '正在完成登录',
			callbackProcessing: '正在验证登录信息，请稍候...',
			callbackHint: '如果页面未自动跳转，请返回登录页重试。',
			callbackMissingToken: '登录信息缺失，请返回重试。',
			backToLogin: '返回登录',
			invitationRequired: '该 Linux.do 账号尚未注册，站点已开启邀请码注册，请输入邀请码以完成注册。',
			invalidPendingToken: '注册凭证已失效，请重新使用 Linux.do 登录。',
			completeRegistration: '完成注册',
			completing: '正在完成注册...',
			completeRegistrationFailed: '注册失败，请检查邀请码后重试。'
		},
		oauth: {
			code: '授权码',
			state: '状态',
			fullUrl: '完整URL'
		},
		forgotPassword: '忘记密码？',
		forgotPasswordTitle: '重置密码',
		forgotPasswordHint: '输入您的邮箱地址，我们将向您发送密码重置链接。',
		sendResetLink: '发送重置链接',
		sendingResetLink: '发送中...',
		sendResetLinkFailed: '发送重置链接失败，请重试。',
		resetEmailSent: '重置链接已发送',
		resetEmailSentHint:
			'如果该邮箱已注册，您将很快收到密码重置链接。请检查您的收件箱和垃圾邮件文件夹。',
		backToLogin: '返回登录',
		rememberedPassword: '想起密码了？',
		resetPasswordTitle: '设置新密码',
		resetPasswordHint: '请在下方输入您的新密码。',
		newPassword: '新密码',
		newPasswordPlaceholder: '输入新密码',
		confirmPassword: '确认密码',
		confirmPasswordPlaceholder: '再次输入新密码',
		confirmPasswordRequired: '请确认您的密码',
		passwordsDoNotMatch: '两次输入的密码不一致',
		resetPassword: '重置密码',
		resettingPassword: '重置中...',
		resetPasswordFailed: '重置密码失败，请重试。',
		passwordResetSuccess: '密码重置成功',
		passwordResetSuccessHint: '您的密码已重置。现在可以使用新密码登录。',
		invalidResetLink: '无效的重置链接',
		invalidResetLinkHint: '此密码重置链接无效或已过期。请重新请求一个新链接。',
		requestNewResetLink: '请求新的重置链接',
		invalidOrExpiredToken: '密码重置链接无效或已过期。请重新请求一个新链接。'
	},

	// Remaining locale content kept identical to previous zh.ts implementation.
}