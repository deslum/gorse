package model

import (
	"fmt"
	"github.com/zhenghaoz/gorse/base"
	"github.com/zhenghaoz/gorse/core"
	"gonum.org/v1/gonum/floats"
	"gonum.org/v1/gonum/mat"
	"log"
	"math"
	"sync"
)

/* SVD */

// SVD algorithm, as popularized by Simon Funk during the
// Netflix Prize. The prediction \hat{r}_{ui} is set as:
//
//               \hat{r}_{ui} = μ + b_u + b_i + q_i^Tp_u
//
// If user u is unknown, then the Bias b_u and the factors p_u are
// assumed to be zero. The same applies for item i with b_i and q_i.
type SVD struct {
	BaseModel
	// Model parameters
	UserFactor [][]float64 // p_u
	ItemFactor [][]float64 // q_i
	UserBias   []float64   // b_u
	ItemBias   []float64   // b_i
	GlobalMean float64     // mu
	// Hyper parameters
	useBias    bool
	nFactors   int
	nEpochs    int
	lr         float64
	reg        float64
	initMean   float64
	initStdDev float64
	batchSize  int
	target     base.ParamString
}

// NewSVD creates a SVD model. Params:
//   UseBias    - Add useBias in SVD model. Default is true.
//	 Reg 		- The regularization parameter of the cost function that is
// 				  optimized. Default is 0.02.
//	 Lr 		- The learning rate of SGD. Default is 0.005.
//	 nFactors	- The number of latent factors. Default is 100.
//	 NEpochs	- The number of iteration of the SGD procedure. Default is 20.
//	 InitMean	- The mean of initial random latent factors. Default is 0.
//	 InitStdDev	- The standard deviation of initial random latent factors. Default is 0.1.
func NewSVD(params base.Params) *SVD {
	svd := new(SVD)
	svd.SetParams(params)
	return svd
}

func (svd *SVD) SetParams(params base.Params) {
	svd.BaseModel.SetParams(params)
	svd.useBias = svd.Params.GetBool(base.UseBias, true)
	svd.nFactors = svd.Params.GetInt(base.NFactors, 100)
	svd.nEpochs = svd.Params.GetInt(base.NEpochs, 20)
	svd.lr = svd.Params.GetFloat64(base.Lr, 0.005)
	svd.reg = svd.Params.GetFloat64(base.Reg, 0.02)
	svd.initMean = svd.Params.GetFloat64(base.InitMean, 0)
	svd.initStdDev = svd.Params.GetFloat64(base.InitStdDev, 0.1)
	svd.target = svd.Params.GetString(base.Target, base.Regression)
}

func (svd *SVD) Predict(userId int, itemId int) float64 {
	denseUserId := svd.UserIdSet.ToDenseId(userId)
	denseItemId := svd.ItemIdSet.ToDenseId(itemId)
	return svd.predict(denseUserId, denseItemId)
}

func (svd *SVD) predict(denseUserId int, denseItemId int) float64 {
	ret := svd.GlobalMean
	// + b_u
	if denseUserId != base.NotId {
		ret += svd.UserBias[denseUserId]
	}
	// + b_i
	if denseItemId != base.NotId {
		ret += svd.ItemBias[denseItemId]
	}
	// + q_i^Tp_u
	if denseItemId != base.NotId && denseUserId != base.NotId {
		userFactor := svd.UserFactor[denseUserId]
		itemFactor := svd.ItemFactor[denseItemId]
		ret += floats.Dot(userFactor, itemFactor)
	}
	return ret
}

func (svd *SVD) Fit(trainSet core.DataSet, options ...base.FitOption) {
	svd.Init(trainSet, options)
	// Initialize parameters
	svd.GlobalMean = 0
	svd.UserBias = make([]float64, trainSet.UserCount())
	svd.ItemBias = make([]float64, trainSet.ItemCount())
	svd.UserFactor = svd.rng.MakeNormalMatrix(trainSet.UserCount(), svd.nFactors, svd.initMean, svd.initStdDev)
	svd.ItemFactor = svd.rng.MakeNormalMatrix(trainSet.ItemCount(), svd.nFactors, svd.initMean, svd.initStdDev)
	// Select fit function
	switch svd.target {
	case base.Regression:
		svd.fitRegression(trainSet)
	case base.BPR:
		svd.fitBPR(trainSet)
	default:
		panic(fmt.Sprintf("Unknown target: %v", svd.target))
	}
}

func (svd *SVD) fitRegression(trainSet core.DataSet) {
	svd.GlobalMean = trainSet.GlobalMean
	// Create buffers
	a := make([]float64, svd.nFactors)
	b := make([]float64, svd.nFactors)
	userFactor := make([]float64, svd.nFactors)
	itemFactor := make([]float64, svd.nFactors)
	// Optimize
	for epoch := 0; epoch < svd.nEpochs; epoch++ {
		perm := svd.rng.Perm(trainSet.Len())
		for _, i := range perm {
			denseUserId, denseItemId, rating := trainSet.GetDense(i)
			// Compute error: e_{ui} = r - \hat r
			upGrad := rating - svd.predict(denseUserId, denseItemId)
			if svd.useBias {
				userBias := svd.UserBias[denseUserId]
				itemBias := svd.ItemBias[denseItemId]
				// Update user Bias: b_u <- b_u + \gamma (e_{ui} - \lambda b_u)
				gradUserBias := upGrad - svd.reg*userBias
				svd.UserBias[denseUserId] += svd.lr * gradUserBias
				// Update item Bias: p_i <- p_i + \gamma (e_{ui} - \lambda b_i)
				gradItemBias := upGrad - svd.reg*itemBias
				svd.ItemBias[denseItemId] += svd.lr * gradItemBias
			}
			copy(userFactor, svd.UserFactor[denseUserId])
			copy(itemFactor, svd.ItemFactor[denseItemId])
			// Update user latent factor
			copy(a, itemFactor)
			base.MulConst(upGrad, a)
			copy(b, userFactor)
			base.MulConst(svd.reg, b)
			floats.Sub(a, b)
			base.MulConst(svd.lr, a)
			floats.Add(svd.UserFactor[denseUserId], a)
			// Update item latent factor
			copy(a, userFactor)
			base.MulConst(upGrad, a)
			copy(b, itemFactor)
			base.MulConst(svd.reg, b)
			floats.Sub(a, b)
			base.MulConst(svd.lr, a)
			floats.Add(svd.ItemFactor[denseItemId], a)
		}
	}
}

func (svd *SVD) fitBPR(trainSet core.DataSet) {
	// Create the set of positive feedback
	positiveSet := make([]map[int]float64, trainSet.UserCount())
	for denseUserId, userRating := range trainSet.DenseUserRatings {
		positiveSet[denseUserId] = make(map[int]float64)
		userRating.ForEach(func(i, index int, value float64) {
			positiveSet[denseUserId][index] = value
		})
	}
	// Create buffers
	a := make([]float64, svd.nFactors)
	b := make([]float64, svd.nFactors)
	userFactor := make([]float64, svd.nFactors)
	positiveItemFactor := make([]float64, svd.nFactors)
	negativeItemFactor := make([]float64, svd.nFactors)
	// Training
	for epoch := 0; epoch < svd.nEpochs; epoch++ {
		// Training epoch
		for i := 0; i < trainSet.Len(); i++ {
			// Select a user
			denseUserId := svd.rng.Intn(trainSet.UserCount())
			densePosId := trainSet.DenseUserRatings[denseUserId].Indices[svd.rng.Intn(trainSet.DenseUserRatings[denseUserId].Len())]
			// Select a negative sample
			denseNegId := -1
			for {
				temp := svd.rng.Intn(trainSet.ItemCount())
				if _, exist := positiveSet[denseUserId][temp]; !exist {
					denseNegId = temp
					break
				}
			}
			diff := svd.predict(denseUserId, densePosId) - svd.predict(denseUserId, denseNegId)
			grad := math.Exp(-diff) / (1.0 + math.Exp(-diff))
			// Pairwise update
			copy(userFactor, svd.UserFactor[denseUserId])
			copy(positiveItemFactor, svd.ItemFactor[densePosId])
			copy(negativeItemFactor, svd.ItemFactor[denseNegId])
			// Update positive item latent factor: +w_u
			copy(a, userFactor)
			base.MulConst(grad, a)
			copy(b, positiveItemFactor)
			base.MulConst(svd.reg, b)
			floats.Sub(a, b)
			base.MulConst(svd.lr, a)
			floats.Add(svd.ItemFactor[densePosId], a)
			// Update negative item latent factor: -w_u
			copy(a, userFactor)
			base.Neg(a)
			base.MulConst(grad, a)
			copy(b, negativeItemFactor)
			base.MulConst(svd.reg, b)
			floats.Sub(a, b)
			base.MulConst(svd.lr, a)
			floats.Add(svd.ItemFactor[denseNegId], a)
			// Update user latent factor: h_i-h_j
			copy(a, positiveItemFactor)
			floats.Sub(a, negativeItemFactor)
			base.MulConst(grad, a)
			copy(b, userFactor)
			base.MulConst(svd.reg, b)
			floats.Sub(a, b)
			base.MulConst(svd.lr, a)
			floats.Add(svd.UserFactor[denseUserId], a)
		}
	}
}

/* NMF */

// NMF: Non-negative Matrix Factorization[3].
type NMF struct {
	BaseModel
	GlobalMean float64
	UserFactor [][]float64 // p_u
	ItemFactor [][]float64 // q_i
	nFactors   int
	nEpochs    int
	initLow    float64
	initHigh   float64
	reg        float64
}

// NewNMF creates a NMF model. Params:
//	 Reg      - The regularization parameter of the cost function that is
//              optimized. Default is 0.06.
//	 NFactors - The number of latent factors. Default is 15.
//	 NEpochs  - The number of iteration of the SGD procedure. Default is 50.
//	 InitLow  - The lower bound of initial random latent factor. Default is 0.
//	 InitHigh - The upper bound of initial random latent factor. Default is 1.
func NewNMF(params base.Params) *NMF {
	nmf := new(NMF)
	nmf.SetParams(params)
	return nmf
}

func (nmf *NMF) SetParams(params base.Params) {
	nmf.BaseModel.SetParams(params)
	nmf.nFactors = nmf.Params.GetInt(base.NFactors, 15)
	nmf.nEpochs = nmf.Params.GetInt(base.NEpochs, 50)
	nmf.initLow = nmf.Params.GetFloat64(base.InitLow, 0)
	nmf.initHigh = nmf.Params.GetFloat64(base.InitHigh, 1)
	nmf.reg = nmf.Params.GetFloat64(base.Reg, 0.06)
}

func (nmf *NMF) Predict(userId int, itemId int) float64 {
	denseUserId := nmf.UserIdSet.ToDenseId(userId)
	denseItemId := nmf.ItemIdSet.ToDenseId(itemId)
	return nmf.predict(denseUserId, denseItemId)
}

func (nmf *NMF) predict(denseUserId int, denseItemId int) float64 {
	if denseItemId != base.NotId && denseUserId != base.NotId {
		return floats.Dot(nmf.UserFactor[denseUserId], nmf.ItemFactor[denseItemId])
	}
	return nmf.GlobalMean
}

func (nmf *NMF) Fit(trainSet core.DataSet, options ...base.FitOption) {
	nmf.Init(trainSet, options)
	// Initialize parameters
	nmf.GlobalMean = trainSet.GlobalMean
	nmf.UserFactor = nmf.rng.MakeUniformMatrix(trainSet.UserCount(), nmf.nFactors, nmf.initLow, nmf.initHigh)
	nmf.ItemFactor = nmf.rng.MakeUniformMatrix(trainSet.ItemCount(), nmf.nFactors, nmf.initLow, nmf.initHigh)
	// Create intermediate matrix buffer
	buffer := make([]float64, nmf.nFactors)
	userNum := base.MakeMatrix(trainSet.UserCount(), nmf.nFactors)
	userDen := base.MakeMatrix(trainSet.UserCount(), nmf.nFactors)
	itemNum := base.MakeMatrix(trainSet.ItemCount(), nmf.nFactors)
	itemDen := base.MakeMatrix(trainSet.ItemCount(), nmf.nFactors)
	// Stochastic Gradient Descent
	for epoch := 0; epoch < nmf.nEpochs; epoch++ {
		// Reset intermediate matrices
		base.FillZeroMatrix(userNum)
		base.FillZeroMatrix(userDen)
		base.FillZeroMatrix(itemNum)
		base.FillZeroMatrix(itemDen)
		// Calculate intermediate matrices
		for i := 0; i < trainSet.Len(); i++ {
			denseUserId, denseItemId, rating := trainSet.GetDense(i)
			prediction := nmf.predict(denseUserId, denseItemId)
			// Update \sum_{i\in{I_u}} q_{if}⋅r_{ui}
			copy(buffer, nmf.ItemFactor[denseItemId])
			base.MulConst(rating, buffer)
			floats.Add(userNum[denseUserId], buffer)
			// Update \sum_{i\in{I_u}} q_{if}⋅\hat{r}_{ui} + \lambda|I_u|p_{uf}
			copy(buffer, nmf.ItemFactor[denseItemId])
			base.MulConst(prediction, buffer)
			floats.Add(userDen[denseUserId], buffer)
			copy(buffer, nmf.UserFactor[denseUserId])
			base.MulConst(nmf.reg, buffer)
			floats.Add(userDen[denseUserId], buffer)
			// Update \sum_{u\in{U_i}}p_{uf}⋅r_{ui}
			copy(buffer, nmf.UserFactor[denseUserId])
			base.MulConst(rating, buffer)
			floats.Add(itemNum[denseItemId], buffer)
			// Update \sum_{u\in{U_i}}p_{uf}⋅\hat{r}_{ui} + \lambda|U_i|q_{if}
			copy(buffer, nmf.UserFactor[denseUserId])
			base.MulConst(prediction, buffer)
			floats.Add(itemDen[denseItemId], buffer)
			copy(buffer, nmf.ItemFactor[denseItemId])
			base.MulConst(nmf.reg, buffer)
			floats.Add(itemDen[denseItemId], buffer)
		}
		// Update user factors
		for u := range nmf.UserFactor {
			copy(buffer, userNum[u])
			floats.Div(buffer, userDen[u])
			floats.Mul(nmf.UserFactor[u], buffer)
		}
		// Update item factors
		for i := range nmf.ItemFactor {
			copy(buffer, itemNum[i])
			floats.Div(buffer, itemDen[i])
			floats.Mul(nmf.ItemFactor[i], buffer)
		}
	}
}

/* SVD++ */

// SVD++ algorithm, an extension of SVD taking into account implicit
// interactionRatings. The prediction \hat{r}_{ui} is set as:
//
// 	\hat{r}_{ui} = \mu + b_u + b_i + q_i^T\left(p_u + |I_u|^{-\frac{1}{2}} \sum_{j \in I_u}y_j\right)
//
// Where the y_j terms are a new set of item factors that capture implicit
// interactionRatings. Here, an implicit rating describes the fact that a user u
// DenseUserRatings an item j, regardless of the rating value. If user u is unknown,
// then the Bias b_u and the factors p_u are assumed to be zero. The same
// applies for item i with b_i, q_i and y_i.
type SVDpp struct {
	BaseModel
	UserRatings []base.SparseVector // I_u
	UserFactor  [][]float64         // p_u
	ItemFactor  [][]float64         // q_i
	ImplFactor  [][]float64         // y_i
	UserBias    []float64           // b_u
	ItemBias    []float64           // b_i
	GlobalMean  float64             // mu
	nFactors    int
	nEpochs     int
	reg         float64
	lr          float64
	initMean    float64
	initStdDev  float64
}

// NewSVDpp creates a SVD++ model. Params:
//	 Reg 		- The regularization parameter of the cost function that is
// 				  optimized. Default is 0.02.
//	 Lr 		- The learning rate of SGD. Default is 0.007.
//	 NFactors	- The number of latent factors. Default is 20.
//	 NEpochs	- The number of iteration of the SGD procedure. Default is 20.
//	 InitMean	- The mean of initial random latent factors. Default is 0.
//	 InitStdDev	- The standard deviation of initial random latent factors. Default is 0.1.
func NewSVDpp(params base.Params) *SVDpp {
	svd := new(SVDpp)
	svd.SetParams(params)
	return svd
}

func (svd *SVDpp) SetParams(params base.Params) {
	svd.BaseModel.SetParams(params)
	// Setup parameters
	svd.nFactors = svd.Params.GetInt(base.NFactors, 20)
	svd.nEpochs = svd.Params.GetInt(base.NEpochs, 20)
	svd.lr = svd.Params.GetFloat64(base.Lr, 0.007)
	svd.reg = svd.Params.GetFloat64(base.Reg, 0.02)
	svd.initMean = svd.Params.GetFloat64(base.InitMean, 0)
	svd.initStdDev = svd.Params.GetFloat64(base.InitStdDev, 0.1)
}

func (svd *SVDpp) Predict(userId int, itemId int) float64 {
	denseUserId := svd.UserIdSet.ToDenseId(userId)
	denseItemId := svd.ItemIdSet.ToDenseId(itemId)
	ret := svd.predict(denseUserId, denseItemId, nil)
	return ret
}

func (svd *SVDpp) predict(denseUserId int, denseItemId int, sumFactor []float64) float64 {
	ret := svd.GlobalMean
	// + b_u
	if denseUserId != base.NotId {
		ret += svd.UserBias[denseUserId]
	}
	// + b_i
	if denseItemId != base.NotId {
		ret += svd.ItemBias[denseItemId]
	}
	// + q_i^T\left(p_u + |I_u|^{-\frac{1}{2}} \sum_{j \in I_u}y_j\right)
	if denseItemId != base.NotId && denseUserId != base.NotId {
		userFactor := svd.UserFactor[denseUserId]
		itemFactor := svd.ItemFactor[denseItemId]
		if len(sumFactor) == 0 {
			sumFactor = svd.getSumFactors(denseUserId)
		}
		temp := make([]float64, len(itemFactor))
		floats.Add(temp, userFactor)
		floats.Add(temp, sumFactor)
		ret += floats.Dot(temp, itemFactor)
	}
	return ret
}

func (svd *SVDpp) getSumFactors(denseUserId int) []float64 {
	sumFactor := make([]float64, svd.nFactors)
	// User history exists
	svd.UserRatings[denseUserId].ForEach(func(i, index int, value float64) {
		floats.Add(sumFactor, svd.ImplFactor[index])
	})
	scale := math.Pow(float64(svd.UserRatings[denseUserId].Len()), -0.5)
	base.MulConst(scale, sumFactor)
	return sumFactor
}

func (svd *SVDpp) Fit(trainSet core.DataSet, setters ...base.FitOption) {
	svd.Init(trainSet, setters)
	// Initialize parameters
	svd.GlobalMean = trainSet.GlobalMean
	svd.UserBias = make([]float64, trainSet.UserCount())
	svd.ItemBias = make([]float64, trainSet.ItemCount())
	svd.UserFactor = svd.rng.MakeNormalMatrix(trainSet.UserCount(), svd.nFactors, svd.initMean, svd.initStdDev)
	svd.ItemFactor = svd.rng.MakeNormalMatrix(trainSet.ItemCount(), svd.nFactors, svd.initMean, svd.initStdDev)
	svd.ImplFactor = svd.rng.MakeNormalMatrix(trainSet.ItemCount(), svd.nFactors, svd.initMean, svd.initStdDev)
	// Build user rating set
	svd.UserRatings = trainSet.DenseUserRatings
	// Create buffers
	a := make([]float64, svd.nFactors)
	b := make([]float64, svd.nFactors)
	step := make([]float64, svd.nFactors)
	userFactor := make([]float64, svd.nFactors)
	itemFactor := make([]float64, svd.nFactors)
	c := base.MakeMatrix(svd.rtOptions.NJobs, svd.nFactors)
	d := base.MakeMatrix(svd.rtOptions.NJobs, svd.nFactors)
	// Stochastic Gradient Descent
	for epoch := 0; epoch < svd.nEpochs; epoch++ {
		for denseUserId := 0; denseUserId < trainSet.UserCount(); denseUserId++ {
			base.FillZeroVector(step)
			size := svd.UserRatings[denseUserId].Len()
			scale := math.Pow(float64(size), -0.5)
			sumFactor := svd.getSumFactors(denseUserId)
			trainSet.DenseUserRatings[denseUserId].ForEach(func(i, denseItemId int, rating float64) {
				userBias := svd.UserBias[denseUserId]
				itemBias := svd.ItemBias[denseItemId]
				copy(userFactor, svd.UserFactor[denseUserId])
				copy(itemFactor, svd.ItemFactor[denseItemId])
				// Compute error: e_{ui} = r - \hat r
				pred := svd.predict(denseUserId, denseItemId, sumFactor)
				diff := rating - pred
				// Update user Bias: b_u <- b_u + \gamma (e_{ui} - \lambda b_u)
				gradUserBias := diff - svd.reg*userBias
				svd.UserBias[denseUserId] += svd.lr * gradUserBias
				// Update item Bias: p_i <- p_i + \gamma (e_{ui} - \lambda b_i)
				gradItemBias := diff - svd.reg*itemBias
				svd.ItemBias[denseItemId] += svd.lr * gradItemBias
				// Update user latent factor
				copy(a, itemFactor)
				base.MulConst(diff, a)
				copy(b, userFactor)
				base.MulConst(svd.reg, b)
				floats.Sub(a, b)
				base.MulConst(svd.lr, a)
				floats.Add(svd.UserFactor[denseUserId], a)
				// Update item latent factor
				copy(a, userFactor)
				floats.Add(a, sumFactor)
				base.MulConst(diff, a)
				copy(b, itemFactor)
				base.MulConst(svd.reg, b)
				floats.Sub(a, b)
				base.MulConst(svd.lr, a)
				floats.Add(svd.ItemFactor[denseItemId], a)
				// Update implicit latent factor: e_{ui}q_j|I_u|^{-1/2}
				copy(a, itemFactor)
				base.MulConst(scale, a)
				base.MulConst(diff, a)
				floats.Add(step, a)
			})
			// Update implicit latent factor
			var wg sync.WaitGroup
			wg.Add(svd.rtOptions.NJobs)
			for j := 0; j < svd.rtOptions.NJobs; j++ {
				go func(jobId int) {
					low := size * jobId / svd.rtOptions.NJobs
					high := size * (jobId + 1) / svd.rtOptions.NJobs
					a := c[jobId]
					b := d[jobId]
					for i := low; i < high; i++ {
						denseItemId := svd.UserRatings[denseUserId].Indices[i]
						implFactor := svd.ImplFactor[denseItemId]
						// a <- e_{ui}q_j|I_u|^{-1/2}
						copy(a, step)
						base.DivConst(float64(size), step)
						// + \lambda y_k
						copy(b, implFactor)
						base.MulConst(svd.reg, b)
						//MulConst(float64(size), b)
						floats.Sub(a, b)
						// \mu (e_{ui}q_j|I_u|^{-1/2} + \lambda y_k)
						base.MulConst(svd.lr, a)
						floats.Add(svd.ImplFactor[denseItemId], a)
					}
					wg.Done()
				}(j)
			}
			//Wait all updates completed
			wg.Wait()
		}
	}
}

// WRMF[7] model for implicit feedback.
type WRMF struct {
	BaseModel
	// Model parameters
	UserFactor *mat.Dense // p_u
	ItemFactor *mat.Dense // q_i
	// Hyper parameters
	nFactors   int
	nEpochs    int
	reg        float64
	initMean   float64
	initStdDev float64
	alpha      float64
}

// NewWRMF creates a WRMF model. Parameters:
//   NFactors   - The number of latent factors. Default is 10.
//   NEpochs    - The number of training epochs. Default is 50.
//   InitMean   - The mean of initial latent factors. Default is 0.
//   InitStdDev - The standard deviation of initial latent factors. Default is 0.1.
//   Reg        - The strength of regularization.
func NewWRMF(params base.Params) *WRMF {
	mf := new(WRMF)
	mf.SetParams(params)
	return mf
}

func (mf *WRMF) SetParams(params base.Params) {
	mf.BaseModel.SetParams(params)
	mf.nFactors = mf.Params.GetInt(base.NFactors, 15)
	mf.nEpochs = mf.Params.GetInt(base.NEpochs, 50)
	mf.initMean = mf.Params.GetFloat64(base.InitMean, 0)
	mf.initStdDev = mf.Params.GetFloat64(base.InitStdDev, 0.1)
	mf.reg = mf.Params.GetFloat64(base.Reg, 0.06)
}

func (mf *WRMF) Predict(userId, itemId int) float64 {
	denseUserId := mf.UserIdSet.ToDenseId(userId)
	denseItemId := mf.ItemIdSet.ToDenseId(itemId)
	if denseUserId == base.NotId || denseItemId == base.NotId {
		return 0
	}
	return mat.Dot(mf.UserFactor.RowView(denseUserId),
		mf.ItemFactor.RowView(denseItemId))
}

func (mf *WRMF) Fit(set core.DataSet, options ...base.FitOption) {
	mf.Init(set, options)
	// Initialize
	mf.UserFactor = mat.NewDense(set.UserCount(), mf.nFactors,
		mf.rng.MakeNormalVector(set.UserCount()*mf.nFactors, mf.initMean, mf.initStdDev))
	mf.ItemFactor = mat.NewDense(set.ItemCount(), mf.nFactors,
		mf.rng.MakeNormalVector(set.ItemCount()*mf.nFactors, mf.initMean, mf.initStdDev))
	// Create temporary matrix
	temp1 := mat.NewDense(mf.nFactors, mf.nFactors, nil)
	temp2 := mat.NewVecDense(mf.nFactors, nil)
	a := mat.NewDense(mf.nFactors, mf.nFactors, nil)
	c := mat.NewDense(mf.nFactors, mf.nFactors, nil)
	p := mat.NewDense(set.UserCount(), set.ItemCount(), nil)
	// Create regularization matrix
	regs := make([]float64, mf.nFactors)
	for i := range regs {
		regs[i] = mf.reg
	}
	regI := mat.NewDiagDense(mf.nFactors, regs)
	for ep := 0; ep < mf.nEpochs; ep++ {
		// Recompute all user factors: x_u = (Y^T C^u Y + \lambda reg)^{-1} Y^T C^u p(u)
		// Y^T Y
		c.Mul(mf.ItemFactor.T(), mf.ItemFactor)
		// X Y^T
		p.Mul(mf.UserFactor, mf.ItemFactor.T())
		for u := 0; u < set.UserCount(); u++ {
			a.Copy(c)
			b := mat.NewVecDense(mf.nFactors, nil)
			set.DenseUserRatings[u].ForEach(func(_, index int, value float64) {
				// Y^T (C^u-I) Y
				weight := value
				temp1.Outer(weight, mf.ItemFactor.RowView(index), mf.ItemFactor.RowView(index))
				a.Add(a, temp1)
				// Y^T C^u p(u)
				temp2.ScaleVec(weight+1, mf.ItemFactor.RowView(index))
				b.AddVec(b, temp2)
			})
			a.Add(a, regI)
			if err := temp1.Inverse(a); err != nil {
				log.Println(err)
				panic("A")
			}
			temp2.MulVec(temp1, b)
			mf.UserFactor.SetRow(u, temp2.RawVector().Data)
		}
		// Recompute all item factors: y_i = (X^T C^i X + \lambda reg)^{-1} X^T C^i p(i)
		// X^T X
		c.Mul(mf.UserFactor.T(), mf.UserFactor)
		// X Y^T
		p.Mul(mf.UserFactor, mf.ItemFactor.T())
		for i := 0; i < set.ItemCount(); i++ {
			a.Copy(c)
			b := mat.NewVecDense(mf.nFactors, nil)
			set.DenseItemRatings[i].ForEach(func(_, index int, value float64) {
				// X^T (C^i-I) X
				weight := value
				temp1.Outer(weight, mf.UserFactor.RowView(index), mf.UserFactor.RowView(index))
				a.Add(a, temp1)
				// X^T C^i p(i)
				temp2.ScaleVec(weight+1, mf.UserFactor.RowView(index))
				b.AddVec(b, temp2)
			})
			a.Add(a, regI)
			if err := temp1.Inverse(a); err != nil {
				log.Println(err)
				panic("B")
			}
			temp2.MulVec(temp1, b)
			mf.ItemFactor.SetRow(i, temp2.RawVector().Data)
		}
	}
}

func (mf *WRMF) weight(value float64) float64 {
	return mf.alpha * value
}
